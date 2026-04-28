package series

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRingSink_RangeWindow(t *testing.T) {
	s := NewRingSink[DigestSample](10)
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		s.Append(DigestSample{
			Time:              base.Add(time.Duration(i) * time.Second),
			SumTimerWaitDelta: uint64(i),
		})
	}

	now := base.Add(9 * time.Second)

	// Window is inclusive on the cutoff side: cutoff = t5,
	// so samples at t=5..9 (five of them) are returned.
	got := collect(s.Range(now, 4*time.Second))
	assert.Len(t, got, 5)
	assert.Equal(t, uint64(5), got[0].SumTimerWaitDelta)
	assert.Equal(t, uint64(9), got[4].SumTimerWaitDelta)

	// Zero window means everything.
	all := collect(s.Range(now, 0))
	assert.Len(t, all, 10)
}

func TestRingSink_RangeIncludesBoundary(t *testing.T) {
	s := NewRingSink[DigestSample](4)
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 4; i++ {
		s.Append(DigestSample{Time: base.Add(time.Duration(i) * time.Second)})
	}

	// Window of exactly 3s ending at t=3 must include t=0..3 inclusive
	// (boundary cutoff = t=0 and Before(cutoff) is false).
	now := base.Add(3 * time.Second)
	got := collect(s.Range(now, 3*time.Second))
	assert.Len(t, got, 4)
}

func TestRingSink_AppendOverflowsWindow(t *testing.T) {
	s := NewRingSink[DigestSample](3)
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		s.Append(DigestSample{Time: base.Add(time.Duration(i) * time.Second)})
	}
	// Only the last 3 are retained.
	all := collect(s.Range(base.Add(4*time.Second), 0))
	assert.Len(t, all, 3)
	assert.Equal(t, base.Add(2*time.Second), all[0].Time)
	assert.Equal(t, base.Add(4*time.Second), all[2].Time)
}

func collect[T any](seq func(yield func(T) bool)) []T {
	var out []T
	for v := range seq {
		out = append(out, v)
	}
	return out
}
