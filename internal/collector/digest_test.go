package collector

import (
	"context"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePerfDB is a hand-rolled stub that returns scripted DigestStats
// results across calls. It only implements what DigestCollector needs.
type fakePerfDB struct {
	digestPolls [][]db.DigestRow
	idx         int
}

func (f *fakePerfDB) DigestStats(_ context.Context) ([]db.DigestRow, error) {
	if f.idx >= len(f.digestPolls) {
		return nil, nil
	}
	out := f.digestPolls[f.idx]
	f.idx++
	return out, nil
}

func (f *fakePerfDB) WaitStats(_ context.Context) ([]db.WaitRow, error) {
	return nil, nil
}
func (f *fakePerfDB) CurrentStatements(_ context.Context) ([]db.CurrentStmt, error) {
	return nil, nil
}
func (f *fakePerfDB) RecentExample(_ context.Context, _ string) (db.Example, error) {
	return db.Example{}, nil
}
func (f *fakePerfDB) ProbeCapabilities(_ context.Context) (db.PerfCapabilities, error) {
	return db.PerfCapabilities{DigestAvailable: true}, nil
}

func TestDigestCollector_FirstPollSeedsOnly(t *testing.T) {
	src := &fakePerfDB{
		digestPolls: [][]db.DigestRow{
			{{Schema: "app", Digest: "abc", DigestText: "SELECT 1", CountStar: 5, SumTimerWait: 1000}},
		},
	}
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 4, SampleCapacity: 4})
	c := NewDigestCollector(src, reg)

	now := time.Now()
	res, err := c.Poll(context.Background(), now)
	require.NoError(t, err)
	assert.Equal(t, 1, res.SeenDigests)
	assert.Equal(t, 0, res.EmittedSamples, "first poll must not emit samples")
	assert.Equal(t, 1, res.BaselineSeeded)

	e := reg.Get(series.DigestKey{Schema: "app", Digest: "abc"})
	require.NotNil(t, e, "registry must have an entry for the seen digest")
	assert.Equal(t, "SELECT 1", e.Text)
	assert.Equal(t, 0, e.Sink.Len(), "first poll seeds but does not append")
}

func TestDigestCollector_SecondPollEmitsDeltas(t *testing.T) {
	src := &fakePerfDB{
		digestPolls: [][]db.DigestRow{
			{{Schema: "app", Digest: "abc", DigestText: "SELECT 1", CountStar: 5, SumTimerWait: 1000, SumRowsExamined: 50}},
			{{Schema: "app", Digest: "abc", DigestText: "SELECT 1", CountStar: 12, SumTimerWait: 4000, SumRowsExamined: 130}},
		},
	}
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 4, SampleCapacity: 4})
	c := NewDigestCollector(src, reg)

	t0 := time.Now()
	_, err := c.Poll(context.Background(), t0)
	require.NoError(t, err)

	t1 := t0.Add(10 * time.Second)
	res, err := c.Poll(context.Background(), t1)
	require.NoError(t, err)
	assert.Equal(t, 1, res.EmittedSamples)
	assert.Equal(t, 0, res.ResetSkips)

	e := reg.Get(series.DigestKey{Schema: "app", Digest: "abc"})
	require.NotNil(t, e)
	require.Equal(t, 1, e.Sink.Len())

	got := drainDigest(e.Sink, t1)
	assert.Equal(t, uint64(7), got[0].ExecCountDelta)
	assert.Equal(t, uint64(3000), got[0].SumTimerWaitDelta)
	assert.Equal(t, uint64(80), got[0].SumRowsExaminedDelta)
	assert.Equal(t, 10*time.Second, got[0].Interval)
}

func TestDigestCollector_CounterResetSkipsAndReseeds(t *testing.T) {
	src := &fakePerfDB{
		digestPolls: [][]db.DigestRow{
			{{Schema: "app", Digest: "abc", CountStar: 1000, SumTimerWait: 100000, SumRowsExamined: 5000}},
			{{Schema: "app", Digest: "abc", CountStar: 5, SumTimerWait: 1000, SumRowsExamined: 50}},      // server restart
			{{Schema: "app", Digest: "abc", CountStar: 10, SumTimerWait: 2000, SumRowsExamined: 100}},   // normal poll
		},
	}
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 4, SampleCapacity: 4})
	c := NewDigestCollector(src, reg)

	t0 := time.Now()
	_, _ = c.Poll(context.Background(), t0)
	res2, _ := c.Poll(context.Background(), t0.Add(10*time.Second))
	assert.Equal(t, 1, res2.ResetSkips)
	assert.Equal(t, 0, res2.EmittedSamples, "reset interval must not emit a bogus sample")

	res3, _ := c.Poll(context.Background(), t0.Add(20*time.Second))
	assert.Equal(t, 1, res3.EmittedSamples, "the next interval after reset must emit a normal delta")
	assert.Equal(t, 0, res3.ResetSkips)

	e := reg.Get(series.DigestKey{Schema: "app", Digest: "abc"})
	got := drainDigest(e.Sink, t0.Add(20*time.Second))
	require.Len(t, got, 1)
	assert.Equal(t, uint64(5), got[0].ExecCountDelta)
}

func TestDigestCollector_DropsDigestsThatDisappear(t *testing.T) {
	src := &fakePerfDB{
		digestPolls: [][]db.DigestRow{
			{{Schema: "app", Digest: "abc", CountStar: 1}, {Schema: "app", Digest: "xyz", CountStar: 1}},
			{{Schema: "app", Digest: "abc", CountStar: 5}}, // xyz gone
			{{Schema: "app", Digest: "abc", CountStar: 6}, {Schema: "app", Digest: "xyz", CountStar: 1}}, // xyz reappears
		},
	}
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 4, SampleCapacity: 4})
	c := NewDigestCollector(src, reg)

	t0 := time.Now()
	_, _ = c.Poll(context.Background(), t0)
	_, _ = c.Poll(context.Background(), t0.Add(10*time.Second))
	res3, _ := c.Poll(context.Background(), t0.Add(20*time.Second))

	// xyz returns with CountStar=1; because its baseline was dropped
	// when it disappeared, this poll should treat it as a fresh seed,
	// not a counter reset.
	assert.Equal(t, 1, res3.BaselineSeeded)
	assert.Equal(t, 0, res3.ResetSkips)
}

func TestDigestCollector_RegistryEvictionUnderCap(t *testing.T) {
	rows := func(n int, base uint64) []db.DigestRow {
		out := make([]db.DigestRow, n)
		for i := 0; i < n; i++ {
			out[i] = db.DigestRow{
				Schema: "app", Digest: digestID(i),
				DigestText: "SELECT 1",
				CountStar:  base,
			}
		}
		return out
	}

	src := &fakePerfDB{digestPolls: [][]db.DigestRow{rows(5, 1)}}
	reg := series.NewRegistry(series.RegistryConfig{
		MaxDigests:          3,
		SampleCapacity:      4,
		NewDigestProtection: 0, // tests need predictable eviction
	})
	c := NewDigestCollector(src, reg)

	_, err := c.Poll(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 3, reg.Len(), "registry must enforce its cap")
	assert.GreaterOrEqual(t, reg.Evicted(), uint64(2))
}

func digestID(i int) string {
	return string(rune('a'+i)) + "-digest"
}

func drainDigest(sink *series.RingSink[series.DigestSample], now time.Time) []series.DigestSample {
	var out []series.DigestSample
	for s := range sink.Range(now, 0) {
		out = append(out, s)
	}
	return out
}
