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

func TestClassifyWaitEvent(t *testing.T) {
	cases := []struct {
		name string
		want series.WaitClass
	}{
		{"wait/io/file/innodb/innodb_data_file", series.WaitClassIO},
		{"wait/io/table/sql/handler", series.WaitClassIO},
		{"wait/lock/table/sql/handler", series.WaitClassLock},
		{"wait/synch/mutex/sql/LOCK_open", series.WaitClassSync},
		{"wait/synch/rwlock/innodb/dict_operation_lock", series.WaitClassSync},
		{"wait/synch/cond/innodb/lock_wait_cond", series.WaitClassSync},
		{"wait/synch/sxlock/innodb/whatever", series.WaitClassSync},
		{"wait/io/socket/sql/server_unix_socket", series.WaitClassNetwork},
		{"wait/io/redo_log_flush", series.WaitClassOther},
		{"idle", series.WaitClassOther},
		{"", series.WaitClassOther},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, ClassifyWaitEvent(c.name), "name=%q", c.name)
	}
}

type fakeWaitDB struct {
	fakePerfDB
	waitPolls [][]db.WaitRow
	waitIdx   int

	currentStmts [][]db.CurrentStmt
	stmtIdx      int
}

func (f *fakeWaitDB) WaitStats(_ context.Context) ([]db.WaitRow, error) {
	if f.waitIdx >= len(f.waitPolls) {
		return nil, nil
	}
	out := f.waitPolls[f.waitIdx]
	f.waitIdx++
	return out, nil
}

func (f *fakeWaitDB) CurrentStatements(_ context.Context) ([]db.CurrentStmt, error) {
	if f.stmtIdx >= len(f.currentStmts) {
		return nil, nil
	}
	out := f.currentStmts[f.stmtIdx]
	f.stmtIdx++
	return out, nil
}

func TestWaitCollector_DiffsAndBucketsByClass(t *testing.T) {
	src := &fakeWaitDB{
		waitPolls: [][]db.WaitRow{
			{
				{EventName: "wait/io/file/innodb/innodb_data_file", CountStar: 100, SumTimerWait: 5_000_000},
				{EventName: "wait/lock/table/sql/handler", CountStar: 5, SumTimerWait: 2_000_000},
				{EventName: "wait/synch/mutex/sql/LOCK_open", CountStar: 50, SumTimerWait: 1_000},
			},
			{
				{EventName: "wait/io/file/innodb/innodb_data_file", CountStar: 200, SumTimerWait: 9_000_000}, // +100, +4M
				{EventName: "wait/lock/table/sql/handler", CountStar: 8, SumTimerWait: 5_000_000},          // +3, +3M
				{EventName: "wait/synch/mutex/sql/LOCK_open", CountStar: 50, SumTimerWait: 1_000},          // unchanged
			},
		},
	}
	w := NewWaitSeries(8)
	c := NewWaitCollector(src, w)

	t0 := time.Now()
	res1, err := c.Poll(context.Background(), t0)
	require.NoError(t, err)
	assert.Equal(t, 0, res1.Emitted, "first poll seeds only")
	assert.Equal(t, 3, res1.NewBaselines)

	t1 := t0.Add(10 * time.Second)
	res2, err := c.Poll(context.Background(), t1)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.ResetSkips)

	io := drainWaits(w.Sink(series.WaitClassIO), t1)
	require.Len(t, io, 1)
	assert.Equal(t, uint64(100), io[0].CountDelta)
	assert.Equal(t, uint64(4_000_000), io[0].SumTimerWaitDelta)

	lock := drainWaits(w.Sink(series.WaitClassLock), t1)
	require.Len(t, lock, 1)
	assert.Equal(t, uint64(3), lock[0].CountDelta)
	assert.Equal(t, uint64(3_000_000), lock[0].SumTimerWaitDelta)

	// Sync class had zero deltas across the interval, so no sample.
	assert.Empty(t, drainWaits(w.Sink(series.WaitClassSync), t1))
}

func TestWaitCollector_HandlesCounterReset(t *testing.T) {
	src := &fakeWaitDB{
		waitPolls: [][]db.WaitRow{
			{{EventName: "wait/io/file/x", CountStar: 1000, SumTimerWait: 5_000_000}},
			{{EventName: "wait/io/file/x", CountStar: 5, SumTimerWait: 1_000}}, // restart
			{{EventName: "wait/io/file/x", CountStar: 12, SumTimerWait: 2_000}},
		},
	}
	w := NewWaitSeries(4)
	c := NewWaitCollector(src, w)

	t0 := time.Now()
	_, _ = c.Poll(context.Background(), t0)
	res2, _ := c.Poll(context.Background(), t0.Add(10*time.Second))
	assert.Equal(t, 1, res2.ResetSkips)
	assert.Equal(t, 0, res2.Emitted)

	res3, _ := c.Poll(context.Background(), t0.Add(20*time.Second))
	assert.Equal(t, 1, res3.Emitted)
}

func TestCPUSampler_Aggregates(t *testing.T) {
	src := &fakeWaitDB{
		currentStmts: [][]db.CurrentStmt{
			{
				{Executing: true, CurrentWait: ""},     // CPU
				{Executing: true, CurrentWait: "idle"}, // CPU
				{Executing: true, CurrentWait: "wait/io/file/x"}, // not CPU
				{Executing: false, CurrentWait: ""},    // not executing
			},
			{
				{Executing: true, CurrentWait: ""},
				{Executing: true, CurrentWait: ""},
				{Executing: true, CurrentWait: ""},
			},
		},
	}
	w := NewWaitSeries(4)
	t0 := time.Now()
	s := NewCPUSampler(src, w, nil, t0)

	require.NoError(t, s.Sample(context.Background(), t0.Add(time.Second)))
	require.NoError(t, s.Sample(context.Background(), t0.Add(2*time.Second)))

	end := t0.Add(10 * time.Second)
	s.Flush(end)

	cpu := drainWaits(w.Sink(series.WaitClassCPU), end)
	require.Len(t, cpu, 1)
	assert.Equal(t, uint64(5), cpu[0].CPUObservations) // 2 + 3
	assert.Equal(t, uint64(2), cpu[0].CPUTicks)
	assert.Equal(t, 10*time.Second, cpu[0].Interval)
}

func TestCPUSampler_FlushNoOpWhenIdle(t *testing.T) {
	src := &fakeWaitDB{}
	w := NewWaitSeries(4)
	s := NewCPUSampler(src, w, nil, time.Now())
	s.Flush(time.Now().Add(time.Second))
	assert.Empty(t, drainWaits(w.Sink(series.WaitClassCPU), time.Now()))
}

func TestCPUSampler_EmitsSessionSamplesWithAppTag(t *testing.T) {
	src := &fakeWaitDB{
		currentStmts: [][]db.CurrentStmt{
			{
				{Executing: true, ProcesslistID: 10, Digest: "d1", SQLText: `/* service='checkout' */ SELECT 1`},
				{Executing: true, ProcesslistID: 11, Digest: "d2", ProgramName: "orders-api"},
				{Executing: false, ProcesslistID: 12},
			},
		},
	}
	w := NewWaitSeries(4)
	sessions := series.NewRingSink[series.SessionSample](16)
	t0 := time.Now()
	s := NewCPUSampler(src, w, sessions, t0)

	require.NoError(t, s.Sample(context.Background(), t0))

	var got []series.SessionSample
	for s := range sessions.Range(t0, 0) {
		got = append(got, s)
	}
	require.Len(t, got, 2, "non-executing session should not be sampled")
	assert.Equal(t, "checkout", got[0].AppTag)
	assert.Equal(t, "orders-api", got[1].AppTag)
}

func drainWaits(sink *series.RingSink[series.WaitSample], now time.Time) []series.WaitSample {
	var out []series.WaitSample
	for s := range sink.Range(now, 0) {
		out = append(out, s)
	}
	return out
}
