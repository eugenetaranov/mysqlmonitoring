package series

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_TouchCreatesEntry(t *testing.T) {
	r := NewRegistry(RegistryConfig{MaxDigests: 4, SampleCapacity: 8})
	now := time.Now()

	e := r.Touch(now, DigestKey{Schema: "db", Digest: "abc"}, "SELECT 1")
	require.NotNil(t, e)
	assert.Equal(t, "SELECT 1", e.Text)
	assert.Equal(t, now, e.FirstSeen)
	assert.Equal(t, 1, r.Len())

	// Re-touching returns the same entry and never bumps FirstSeen.
	again := r.Touch(now.Add(time.Minute), DigestKey{Schema: "db", Digest: "abc"}, "")
	assert.Same(t, e, again)
	assert.Equal(t, now, again.FirstSeen)
}

func TestRegistry_TextLazyFill(t *testing.T) {
	r := NewRegistry(RegistryConfig{MaxDigests: 2, SampleCapacity: 4})
	now := time.Now()
	key := DigestKey{Digest: "x"}

	e := r.Touch(now, key, "")
	assert.Empty(t, e.Text)

	// A later Touch with text fills it in.
	r.Touch(now.Add(time.Second), key, "SELECT 2")
	assert.Equal(t, "SELECT 2", r.Get(key).Text)
}

func TestRegistry_AppendAccumulatesLoad(t *testing.T) {
	r := NewRegistry(RegistryConfig{MaxDigests: 4, SampleCapacity: 4})
	key := DigestKey{Digest: "abc"}
	now := time.Now()

	r.Append(key, "SELECT 1", DigestSample{Time: now, SumTimerWaitDelta: 100})
	r.Append(key, "", DigestSample{Time: now.Add(time.Second), SumTimerWaitDelta: 50})

	e := r.Get(key)
	require.NotNil(t, e)
	assert.Equal(t, uint64(150), e.LoadPicos)
	assert.Equal(t, 2, e.Sink.Len())
}

func TestRegistry_EvictsLowestLoadAfterProtection(t *testing.T) {
	r := NewRegistry(RegistryConfig{
		MaxDigests:          2,
		SampleCapacity:      4,
		NewDigestProtection: 30 * time.Second,
	})
	t0 := time.Now()

	// Two old, eligible digests: hi (high load) and lo (low load).
	r.Append(DigestKey{Digest: "hi"}, "", DigestSample{Time: t0, SumTimerWaitDelta: 1000})
	r.Append(DigestKey{Digest: "lo"}, "", DigestSample{Time: t0, SumTimerWaitDelta: 1})

	// Move time forward past the protection window, then admit a new one.
	tNew := t0.Add(time.Minute)
	r.Touch(tNew, DigestKey{Digest: "new"}, "SELECT 3")

	assert.Equal(t, 2, r.Len())
	assert.Nil(t, r.Get(DigestKey{Digest: "lo"}), "low-load digest must be evicted")
	assert.NotNil(t, r.Get(DigestKey{Digest: "hi"}))
	assert.NotNil(t, r.Get(DigestKey{Digest: "new"}))
	assert.Equal(t, uint64(1), r.Evicted())
}

func TestRegistry_ProtectsBrandNewDigests(t *testing.T) {
	r := NewRegistry(RegistryConfig{
		MaxDigests:          2,
		SampleCapacity:      4,
		NewDigestProtection: 30 * time.Second,
	})
	t0 := time.Now()

	// Two digests added at t0; cap is 2.
	r.Touch(t0, DigestKey{Digest: "a"}, "")
	r.Touch(t0, DigestKey{Digest: "b"}, "")

	// Admit a new one only 1s later — neither incumbent is eligible.
	// We still need to make room, so the fallback (oldest) wins.
	r.Touch(t0.Add(time.Second), DigestKey{Digest: "c"}, "")

	assert.Equal(t, 2, r.Len())
	// The fallback evicts whichever was reached first in iteration; we
	// only assert that "c" is now tracked and exactly one of {a,b} is gone.
	assert.NotNil(t, r.Get(DigestKey{Digest: "c"}))
	assert.True(t, (r.Get(DigestKey{Digest: "a"}) == nil) != (r.Get(DigestKey{Digest: "b"}) == nil),
		"exactly one of the protected digests should have been evicted as fallback")
	assert.Equal(t, uint64(1), r.Evicted())
}

func TestRegistry_Snapshot(t *testing.T) {
	r := NewRegistry(RegistryConfig{MaxDigests: 4, SampleCapacity: 4})
	now := time.Now()
	r.Touch(now, DigestKey{Digest: "a"}, "")
	r.Touch(now, DigestKey{Digest: "b"}, "")

	snap := r.Snapshot()
	assert.Len(t, snap, 2)
}
