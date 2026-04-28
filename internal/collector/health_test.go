package collector

import (
	"context"
	"errors"
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHealthSource is a hand-written fake to exercise HealthCollector
// without spinning up MySQL. It records what the collector asked for
// and returns canned replies.
type fakeHealthSource struct {
	probe       db.ReplicaProbe
	probeErr    error
	probeCalls  int
	vitals      []db.HealthVitals // returned in order, last value repeated
	vitalsErrs  []error
	vitalsCalls int
	lastProbe   db.ReplicaProbe // probe arg from last HealthVitals call
	lastPrior   uint64
}

func (f *fakeHealthSource) ProbeReplica(_ context.Context) (db.ReplicaProbe, error) {
	f.probeCalls++
	if f.probeErr != nil {
		return db.ReplicaProbe{}, f.probeErr
	}
	return f.probe, nil
}

func (f *fakeHealthSource) HealthVitals(_ context.Context, probe db.ReplicaProbe, priorAborted uint64) (db.HealthVitals, error) {
	f.lastProbe = probe
	f.lastPrior = priorAborted
	idx := f.vitalsCalls
	f.vitalsCalls++
	if idx < len(f.vitalsErrs) && f.vitalsErrs[idx] != nil {
		return db.HealthVitals{}, f.vitalsErrs[idx]
	}
	if idx >= len(f.vitals) {
		idx = len(f.vitals) - 1
	}
	v := f.vitals[idx]
	// Mimic MySQLDB.HealthVitals: compute the delta against priorAborted
	// since the fake's caller (HealthCollector) is testing this path.
	if v.AbortedClients >= priorAborted {
		v.AbortedClientsDelta = v.AbortedClients - priorAborted
	}
	return v, nil
}

func TestHealthCollector_FirstPollProbesAndDelta0(t *testing.T) {
	f := &fakeHealthSource{
		probe: db.ReplicaProbe{Role: db.ReplicaRoleStandalone, Dialect: db.ReplicaDialectReplica},
		vitals: []db.HealthVitals{
			{ThreadsRunning: 3, AbortedClients: 17},
		},
	}
	h := NewHealthCollector(f)

	v, err := h.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(3), v.ThreadsRunning)
	assert.Equal(t, uint64(17), v.AbortedClients)
	// First poll: priorAborted is zero, so delta == raw counter.
	assert.Equal(t, uint64(17), v.AbortedClientsDelta)
	assert.Equal(t, 1, f.probeCalls)
	assert.Equal(t, db.ReplicaRoleStandalone, h.ReplicaProbe().Role)
}

func TestHealthCollector_ProbeOnlyOnce(t *testing.T) {
	f := &fakeHealthSource{
		probe: db.ReplicaProbe{Role: db.ReplicaRoleReplica, Dialect: db.ReplicaDialectReplica},
		vitals: []db.HealthVitals{
			{ThreadsRunning: 1},
			{ThreadsRunning: 2},
			{ThreadsRunning: 3},
		},
	}
	h := NewHealthCollector(f)

	for i := 0; i < 3; i++ {
		_, err := h.Poll(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, 1, f.probeCalls)
	assert.Equal(t, 3, f.vitalsCalls)
}

func TestHealthCollector_DeltaAcrossPolls(t *testing.T) {
	f := &fakeHealthSource{
		probe: db.ReplicaProbe{Role: db.ReplicaRoleStandalone},
		vitals: []db.HealthVitals{
			{AbortedClients: 100},
			{AbortedClients: 105},
			{AbortedClients: 110},
		},
	}
	h := NewHealthCollector(f)

	v1, _ := h.Poll(context.Background())
	v2, _ := h.Poll(context.Background())
	v3, _ := h.Poll(context.Background())

	assert.Equal(t, uint64(100), v1.AbortedClientsDelta) // first poll vs prior=0
	assert.Equal(t, uint64(5), v2.AbortedClientsDelta)
	assert.Equal(t, uint64(5), v3.AbortedClientsDelta)
}

func TestHealthCollector_CounterResetClampsToZero(t *testing.T) {
	f := &fakeHealthSource{
		probe: db.ReplicaProbe{Role: db.ReplicaRoleStandalone},
		vitals: []db.HealthVitals{
			{AbortedClients: 1000},
			{AbortedClients: 5}, // server restart
		},
	}
	h := NewHealthCollector(f)

	_, _ = h.Poll(context.Background())
	v, err := h.Poll(context.Background())
	require.NoError(t, err)
	// On counter reset, delta is 0 (handled in MySQLDB.HealthVitals;
	// the fake mirrors that contract).
	assert.Equal(t, uint64(0), v.AbortedClientsDelta)
	// Subsequent polls should see proper deltas relative to the new baseline.
	f.vitals = append(f.vitals, db.HealthVitals{AbortedClients: 12})
	v2, _ := h.Poll(context.Background())
	assert.Equal(t, uint64(7), v2.AbortedClientsDelta)
}

func TestHealthCollector_ProbeFailureRetriesNextPoll(t *testing.T) {
	f := &fakeHealthSource{
		probe:    db.ReplicaProbe{Role: db.ReplicaRoleReplica},
		probeErr: errors.New("permission denied"),
		vitals: []db.HealthVitals{
			{ThreadsRunning: 1},
		},
	}
	h := NewHealthCollector(f)

	// First poll: probe fails but Poll continues with zero-value probe.
	v, err := h.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), v.ThreadsRunning)
	assert.Equal(t, db.ReplicaRoleUnknown, h.ReplicaProbe().Role)
	assert.Equal(t, 1, f.probeCalls)

	// Probe succeeds on second poll once the underlying issue is fixed.
	f.probeErr = nil
	_, err = h.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, db.ReplicaRoleReplica, h.ReplicaProbe().Role)
	assert.Equal(t, 2, f.probeCalls)
}

func TestHealthCollector_ErrorLeavesCacheIntact(t *testing.T) {
	good := db.HealthVitals{ThreadsRunning: 5}
	f := &fakeHealthSource{
		probe:      db.ReplicaProbe{Role: db.ReplicaRoleStandalone},
		vitals:     []db.HealthVitals{good, good},
		vitalsErrs: []error{nil, errors.New("transient")},
	}
	h := NewHealthCollector(f)

	v1, err := h.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(5), v1.ThreadsRunning)
	assert.Equal(t, uint64(5), h.Latest().ThreadsRunning)

	_, err = h.Poll(context.Background())
	require.Error(t, err)
	// Latest must remain the last successful snapshot.
	assert.Equal(t, uint64(5), h.Latest().ThreadsRunning)
}

func TestHealthCollector_ProbePropagatedToVitals(t *testing.T) {
	f := &fakeHealthSource{
		probe: db.ReplicaProbe{Role: db.ReplicaRoleReplica, Dialect: db.ReplicaDialectSlave},
		vitals: []db.HealthVitals{
			{ThreadsRunning: 0},
		},
	}
	h := NewHealthCollector(f)
	_, err := h.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, db.ReplicaRoleReplica, f.lastProbe.Role)
	assert.Equal(t, db.ReplicaDialectSlave, f.lastProbe.Dialect)
}
