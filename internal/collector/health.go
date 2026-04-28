package collector

import (
	"context"
	"sync"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// HealthSource is the narrow database interface the HealthCollector
// depends on, separate from the broader db.DB interface so tests can
// fake just these methods.
type HealthSource interface {
	HealthVitals(ctx context.Context, probe db.ReplicaProbe, priorAborted uint64) (db.HealthVitals, error)
	ProbeReplica(ctx context.Context) (db.ReplicaProbe, error)
}

// HealthCollector polls SHOW GLOBAL STATUS cherry-picks and (when the
// server has a replica role) SHOW REPLICA STATUS once per interval.
// The collector caches the role probe — the role of a server doesn't
// flip mid-process, so we save the round-trip on every subsequent poll.
//
// Latest() returns the most recent successful poll's snapshot; the
// zero value is returned before the first poll completes.
type HealthCollector struct {
	source HealthSource

	mu     sync.Mutex
	probe  db.ReplicaProbe
	probed bool
	prior  db.HealthVitals
	latest db.HealthVitals
}

// NewHealthCollector constructs a collector wired to source.
func NewHealthCollector(source HealthSource) *HealthCollector {
	return &HealthCollector{source: source}
}

// Poll runs one collection cycle. Errors are returned to the caller so
// they can be counted; on error the cached snapshot is left unchanged.
func (h *HealthCollector) Poll(ctx context.Context) (db.HealthVitals, error) {
	h.mu.Lock()
	if !h.probed {
		// Probe role on first poll, blocking only this goroutine. If it
		// errors we treat the server as standalone and try again later.
		h.mu.Unlock()
		probe, err := h.source.ProbeReplica(ctx)
		h.mu.Lock()
		if err == nil {
			h.probe = probe
			h.probed = true
		}
	}
	probe := h.probe
	priorAborted := h.prior.AbortedClients
	h.mu.Unlock()

	v, err := h.source.HealthVitals(ctx, probe, priorAborted)
	if err != nil {
		return db.HealthVitals{}, err
	}

	h.mu.Lock()
	h.prior = v
	h.latest = v
	h.mu.Unlock()
	return v, nil
}

// Latest returns the most recent successful snapshot. Callers must
// treat a zero Time as "no data yet".
func (h *HealthCollector) Latest() db.HealthVitals {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latest
}

// ReplicaProbe returns the cached probe result. Role is
// ReplicaRoleUnknown until the first successful poll.
func (h *HealthCollector) ReplicaProbe() db.ReplicaProbe {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.probed {
		return db.ReplicaProbe{}
	}
	return h.probe
}
