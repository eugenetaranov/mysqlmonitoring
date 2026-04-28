package series

import "iter"

// RingBuffer is a fixed-capacity FIFO that overwrites its oldest entry
// when full. Append, Len and the iterators are all O(1) per operation;
// the buffer never reallocates after construction. RingBuffer is not
// safe for concurrent use — wrap it in a sink that owns the lock.
type RingBuffer[T any] struct {
	buf  []T
	head int  // next write position
	full bool // distinguishes empty from completely-wrapped state
}

// NewRingBuffer returns a buffer that holds up to capacity items.
// A zero or negative capacity is treated as 1 to keep the type total.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &RingBuffer[T]{buf: make([]T, capacity)}
}

// Cap returns the maximum number of items the buffer can hold.
func (r *RingBuffer[T]) Cap() int { return len(r.buf) }

// Len returns the current number of items, in [0, Cap()].
func (r *RingBuffer[T]) Len() int {
	if r.full {
		return len(r.buf)
	}
	return r.head
}

// Append writes v as the newest item, evicting the oldest if the
// buffer is already full.
func (r *RingBuffer[T]) Append(v T) {
	r.buf[r.head] = v
	r.head++
	if r.head == len(r.buf) {
		r.head = 0
		r.full = true
	}
}

// All yields every retained item in chronological order
// (oldest first). It is safe to break out of the loop early.
func (r *RingBuffer[T]) All() iter.Seq[T] {
	return func(yield func(T) bool) {
		n := r.Len()
		if n == 0 {
			return
		}
		start := 0
		if r.full {
			start = r.head
		}
		for i := 0; i < n; i++ {
			if !yield(r.buf[(start+i)%len(r.buf)]) {
				return
			}
		}
	}
}

// Newest returns the most recently appended item. The boolean is
// false when the buffer is empty.
func (r *RingBuffer[T]) Newest() (T, bool) {
	var zero T
	if r.Len() == 0 {
		return zero, false
	}
	idx := r.head - 1
	if idx < 0 {
		idx = len(r.buf) - 1
	}
	return r.buf[idx], true
}

// Reset drops all retained items without reallocating.
func (r *RingBuffer[T]) Reset() {
	r.head = 0
	r.full = false
}
