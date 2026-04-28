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

func TestLoadByGroup_Empty(t *testing.T) {
	sessions := series.NewRingSink[series.SessionSample](8)
	got := LoadByGroup(sessions, time.Now(), time.Minute, GroupKeyUser)
	assert.Empty(t, got)
}

func TestLoadByGroup_NilSessionsSafe(t *testing.T) {
	got := LoadByGroup(nil, time.Now(), time.Minute, GroupKeyUser)
	assert.Nil(t, got)
}

func TestLoadByGroup_SingleDominantGroup(t *testing.T) {
	sessions := series.NewRingSink[series.SessionSample](32)
	now := time.Now()
	// Three ticks; same user holds two executing sessions on each tick.
	for i := 0; i < 3; i++ {
		tick := now.Add(time.Duration(-i) * time.Second)
		sessions.Append(series.SessionSample{Time: tick, User: "app_rw", Executing: true})
		sessions.Append(series.SessionSample{Time: tick, User: "app_rw", Executing: true})
	}

	got := LoadByGroup(sessions, now, time.Minute, GroupKeyUser)
	require.Len(t, got, 1)
	assert.Equal(t, "app_rw", got[0].Group)
	// 6 obs over 3 ticks = AAS 2.0.
	assert.InDelta(t, 2.0, got[0].AAS, 0.001)
	assert.Equal(t, uint64(6), got[0].Observations)
}

func TestLoadByGroup_ManyGroupsSortedByAAS(t *testing.T) {
	sessions := series.NewRingSink[series.SessionSample](64)
	now := time.Now()
	// 4 ticks total. Distribute executing sessions:
	// app_rw : 4 obs (1 per tick)         → AAS 1.0
	// reports: 2 obs (across 2 ticks)     → AAS 0.5
	// app_ro : 1 obs (1 tick)             → AAS 0.25
	for i := 0; i < 4; i++ {
		tick := now.Add(time.Duration(-i) * time.Second)
		sessions.Append(series.SessionSample{Time: tick, User: "app_rw", Executing: true})
	}
	for i := 0; i < 2; i++ {
		tick := now.Add(time.Duration(-i) * time.Second)
		sessions.Append(series.SessionSample{Time: tick, User: "reports", Executing: true})
	}
	sessions.Append(series.SessionSample{Time: now, User: "app_ro", Executing: true})

	got := LoadByGroup(sessions, now, time.Minute, GroupKeyUser)
	require.Len(t, got, 3)
	assert.Equal(t, "app_rw", got[0].Group)
	assert.Equal(t, "reports", got[1].Group)
	assert.Equal(t, "app_ro", got[2].Group)
	assert.InDelta(t, 1.0, got[0].AAS, 0.001)
	assert.InDelta(t, 0.5, got[1].AAS, 0.001)
	assert.InDelta(t, 0.25, got[2].AAS, 0.001)
}

func TestLoadByGroup_SumEqualsTotalAAS(t *testing.T) {
	sessions := series.NewRingSink[series.SessionSample](64)
	now := time.Now()
	for i := 0; i < 5; i++ {
		tick := now.Add(time.Duration(-i) * time.Second)
		sessions.Append(series.SessionSample{Time: tick, User: "u1", Executing: true})
		sessions.Append(series.SessionSample{Time: tick, User: "u2", Executing: true})
		sessions.Append(series.SessionSample{Time: tick, User: "u3", Executing: true})
	}
	got := LoadByGroup(sessions, now, time.Minute, GroupKeyUser)
	var sum float64
	for _, g := range got {
		sum += g.AAS
	}
	// 15 obs / 5 ticks = AAS 3.0 total.
	assert.InDelta(t, 3.0, sum, 0.001)
}

func TestLoadByGroup_UnknownBucket(t *testing.T) {
	sessions := series.NewRingSink[series.SessionSample](16)
	now := time.Now()
	// System / replication thread with NULL user surfaces as "(unknown)".
	sessions.Append(series.SessionSample{Time: now, User: "", Executing: true})
	sessions.Append(series.SessionSample{Time: now, User: "app_rw", Executing: true})

	got := LoadByGroup(sessions, now, time.Minute, GroupKeyUser)
	require.Len(t, got, 2)
	groups := map[string]bool{got[0].Group: true, got[1].Group: true}
	assert.True(t, groups["(unknown)"])
	assert.True(t, groups["app_rw"])
}

func TestLoadByGroup_GroupByHostAndSchema(t *testing.T) {
	sessions := series.NewRingSink[series.SessionSample](32)
	now := time.Now()
	sessions.Append(series.SessionSample{Time: now, Host: "10.0.4.12", Schema: "shop", Executing: true})
	sessions.Append(series.SessionSample{Time: now, Host: "10.0.4.12", Schema: "shop", Executing: true})
	sessions.Append(series.SessionSample{Time: now, Host: "10.0.4.13", Schema: "auth", Executing: true})

	byHost := LoadByGroup(sessions, now, time.Minute, GroupKeyHost)
	require.Len(t, byHost, 2)
	assert.Equal(t, "10.0.4.12", byHost[0].Group)
	assert.Equal(t, uint64(2), byHost[0].Observations)

	bySchema := LoadByGroup(sessions, now, time.Minute, GroupKeySchema)
	require.Len(t, bySchema, 2)
	assert.Equal(t, "shop", bySchema[0].Group)
	assert.Equal(t, uint64(2), bySchema[0].Observations)
}

func TestLoadByGroup_NonExecutingNotCounted(t *testing.T) {
	sessions := series.NewRingSink[series.SessionSample](16)
	now := time.Now()
	// Tick exists (so ticks=1) but no executing sessions.
	sessions.Append(series.SessionSample{Time: now, User: "app_rw", Executing: false})

	got := LoadByGroup(sessions, now, time.Minute, GroupKeyUser)
	assert.Empty(t, got)
}
