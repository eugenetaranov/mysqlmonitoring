package series

import (
	"iter"
	"sync"
	"time"
)

// Sink is the persistence-agnostic seam between collectors and views.
// M1 ships only RingSink; M2's on-disk store will satisfy the same
// interface so collectors and aggregators do not change.
type Sink[T Sample] interface {
	// Append stores s as the newest sample.
	Append(s T)
	// Range yields samples whose SampleTime is within window of now.
	// A zero window means "every retained sample".
	Range(now time.Time, window time.Duration) iter.Seq[T]
	// Len returns the number of currently retained samples.
	Len() int
}

// RingSink wraps a RingBuffer with a mutex so collector writers and
// TUI readers can share it. It is the only Sink implementation in M1.
type RingSink[T Sample] struct {
	mu  sync.RWMutex
	ring *RingBuffer[T]
}

// NewRingSink builds a RingSink with the given fixed capacity.
func NewRingSink[T Sample](capacity int) *RingSink[T] {
	return &RingSink[T]{ring: NewRingBuffer[T](capacity)}
}

// Append stores a sample. Safe for one writer.
func (s *RingSink[T]) Append(v T) {
	s.mu.Lock()
	s.ring.Append(v)
	s.mu.Unlock()
}

// Len returns the current sample count.
func (s *RingSink[T]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ring.Len()
}

// Range yields samples with SampleTime in [now-window, now]. A zero
// window means "yield every retained sample". The iterator copies
// matching samples into a private slice under the lock and yields
// from that, so callers may take their time without blocking writers.
func (s *RingSink[T]) Range(now time.Time, window time.Duration) iter.Seq[T] {
	cutoff := time.Time{}
	if window > 0 {
		cutoff = now.Add(-window)
	}

	s.mu.RLock()
	out := make([]T, 0, s.ring.Len())
	for v := range s.ring.All() {
		if window > 0 && v.SampleTime().Before(cutoff) {
			continue
		}
		out = append(out, v)
	}
	s.mu.RUnlock()

	return func(yield func(T) bool) {
		for _, v := range out {
			if !yield(v) {
				return
			}
		}
	}
}

// Reset drops every retained sample.
func (s *RingSink[T]) Reset() {
	s.mu.Lock()
	s.ring.Reset()
	s.mu.Unlock()
}
