package insights

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubSource struct {
	mu      sync.Mutex
	caps    db.PerfCapabilities
	digests [][]db.DigestRow
	waits   [][]db.WaitRow
	stmts   [][]db.CurrentStmt
	dIdx, wIdx, sIdx int
}

func (s *stubSource) DigestStats(_ context.Context) ([]db.DigestRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dIdx >= len(s.digests) {
		return nil, nil
	}
	out := s.digests[s.dIdx]
	s.dIdx++
	return out, nil
}

func (s *stubSource) WaitStats(_ context.Context) ([]db.WaitRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wIdx >= len(s.waits) {
		return nil, nil
	}
	out := s.waits[s.wIdx]
	s.wIdx++
	return out, nil
}

func (s *stubSource) CurrentStatements(_ context.Context) ([]db.CurrentStmt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sIdx >= len(s.stmts) {
		return nil, nil
	}
	out := s.stmts[s.sIdx]
	s.sIdx++
	return out, nil
}

func (s *stubSource) RecentExample(_ context.Context, _ string) (db.Example, error) {
	return db.Example{}, nil
}

func (s *stubSource) ProbeCapabilities(_ context.Context) (db.PerfCapabilities, error) {
	return s.caps, nil
}

func TestInsights_NewSizesBuffersFromWindow(t *testing.T) {
	cfg := Config{PollInterval: 10 * time.Second, Window: 1 * time.Hour}
	i := New(cfg, &stubSource{})
	require.NotNil(t, i)
	assert.NotNil(t, i.Registry)
	assert.NotNil(t, i.Waits)
	assert.NotNil(t, i.Sessions)
}

func TestInsights_ProbeWritesWarnings(t *testing.T) {
	src := &stubSource{caps: db.PerfCapabilities{
		DigestAvailable: false,
		Warnings:        []string{"statements_digest is OFF"},
	}}
	i := New(DefaultConfig(), src)
	var buf bytes.Buffer

	require.NoError(t, i.Probe(context.Background(), &buf))
	assert.True(t, strings.Contains(buf.String(), "statements_digest"))

	// Second probe must not duplicate output.
	buf.Reset()
	require.NoError(t, i.Probe(context.Background(), &buf))
	assert.Empty(t, buf.String(), "Probe must be idempotent")
}

func TestInsights_RunRespectsCapabilityFlags(t *testing.T) {
	// Both subsystems off → Run returns immediately.
	src := &stubSource{caps: db.PerfCapabilities{}}
	i := New(Config{
		PollInterval:      50 * time.Millisecond,
		CPUSampleInterval: 25 * time.Millisecond,
		Window:            time.Second,
	}, src)
	require.NoError(t, i.Probe(context.Background(), nil))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() { i.Run(ctx); close(done) }()
	select {
	case <-done:
		// good — no goroutines were started
	case <-time.After(150 * time.Millisecond):
		t.Fatal("Run blocked despite no enabled capabilities")
	}
}
