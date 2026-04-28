package insights

import (
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/collector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTopSQL_RanksByAAS(t *testing.T) {
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 8, SampleCapacity: 16})
	now := time.Now()

	// One digest at high load, one at low load, one idle.
	reg.Append(series.DigestKey{Digest: "hi"}, "SELECT hi", series.DigestSample{
		Time: now, Interval: 10 * time.Second,
		ExecCountDelta:    20,
		SumTimerWaitDelta: 30 * 1e12, // 30 seconds of wait → AAS 3.0 over 10s
	})
	reg.Append(series.DigestKey{Digest: "lo"}, "SELECT lo", series.DigestSample{
		Time: now, Interval: 10 * time.Second,
		ExecCountDelta:    100,
		SumTimerWaitDelta: 1 * 1e12, // AAS 0.1
	})
	// Idle digest registered but never appended; survives summarise filter.

	sessions := series.NewRingSink[series.SessionSample](4)
	got := TopSQL(reg, sessions, now, TopSQLOptions{Window: time.Minute, Limit: 5})
	require.Len(t, got, 2)
	assert.Equal(t, "hi", got[0].Digest)
	assert.Equal(t, "lo", got[1].Digest)
	assert.InDelta(t, 3.0, got[0].AAS, 0.01)
	assert.Equal(t, uint64(20), got[0].Calls)
	assert.Equal(t, 1500*time.Millisecond, got[0].AvgLatency)
}

func TestTopSQL_AppFilterScopesByDigest(t *testing.T) {
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 8, SampleCapacity: 16})
	now := time.Now()
	reg.Append(series.DigestKey{Digest: "checkout"}, "SELECT 1", series.DigestSample{
		Time: now, Interval: 10 * time.Second, ExecCountDelta: 5, SumTimerWaitDelta: 1e12,
	})
	reg.Append(series.DigestKey{Digest: "other"}, "SELECT 2", series.DigestSample{
		Time: now, Interval: 10 * time.Second, ExecCountDelta: 50, SumTimerWaitDelta: 5e12,
	})

	sessions := series.NewRingSink[series.SessionSample](16)
	sessions.Append(series.SessionSample{Time: now, Digest: "checkout", AppTag: "checkout"})

	got := TopSQL(reg, sessions, now, TopSQLOptions{Window: time.Minute, App: "checkout"})
	require.Len(t, got, 1)
	assert.Equal(t, "checkout", got[0].Digest)
}

func TestTopSQL_LimitTruncates(t *testing.T) {
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 16, SampleCapacity: 4})
	now := time.Now()
	for i := 0; i < 5; i++ {
		reg.Append(series.DigestKey{Digest: digestID(i)}, "SELECT 1", series.DigestSample{
			Time: now, Interval: 10 * time.Second, ExecCountDelta: uint64(i + 1), SumTimerWaitDelta: uint64(i+1) * 1e12,
		})
	}
	got := TopSQL(reg, nil, now, TopSQLOptions{Window: time.Minute, Limit: 3})
	assert.Len(t, got, 3)
}

func TestLoad_PerClassAAS(t *testing.T) {
	w := collector.NewWaitSeries(8)
	now := time.Now()
	// 2 seconds of IO time over a 10-second interval → AAS 0.2.
	w.Append(series.WaitSample{
		Time: now, Interval: 10 * time.Second, Class: series.WaitClassIO,
		SumTimerWaitDelta: 2 * 1e12,
	})
	// 30 CPU observations over 10 ticks → AAS 3.0.
	w.Append(series.WaitSample{
		Time: now, Interval: 10 * time.Second, Class: series.WaitClassCPU,
		CPUObservations: 30, CPUTicks: 10,
	})
	got := Load(w, now, time.Minute)
	classes := map[series.WaitClass]float64{}
	for _, c := range got.Classes {
		classes[c.Class] = c.AAS
	}
	assert.InDelta(t, 0.2, classes[series.WaitClassIO], 0.01)
	assert.InDelta(t, 3.0, classes[series.WaitClassCPU], 0.01)
	assert.InDelta(t, 3.2, got.Total, 0.01)
}

func digestID(i int) string {
	return string(rune('a'+i)) + "-digest"
}
