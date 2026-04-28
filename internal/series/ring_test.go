package series

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRingBuffer_EmptyAndFill(t *testing.T) {
	r := NewRingBuffer[int](3)
	assert.Equal(t, 3, r.Cap())
	assert.Equal(t, 0, r.Len())

	_, ok := r.Newest()
	assert.False(t, ok)

	r.Append(1)
	r.Append(2)
	assert.Equal(t, 2, r.Len())

	got := drain(r)
	assert.Equal(t, []int{1, 2}, got)

	v, ok := r.Newest()
	require.True(t, ok)
	assert.Equal(t, 2, v)
}

func TestRingBuffer_OverflowDropsOldest(t *testing.T) {
	r := NewRingBuffer[int](3)
	for i := 1; i <= 5; i++ {
		r.Append(i)
	}
	assert.Equal(t, 3, r.Len())
	assert.Equal(t, []int{3, 4, 5}, drain(r))

	v, _ := r.Newest()
	assert.Equal(t, 5, v)
}

func TestRingBuffer_AllRespectsBreak(t *testing.T) {
	r := NewRingBuffer[int](4)
	for i := 1; i <= 4; i++ {
		r.Append(i)
	}

	var seen []int
	for v := range r.All() {
		seen = append(seen, v)
		if v == 2 {
			break
		}
	}
	assert.Equal(t, []int{1, 2}, seen)
}

func TestRingBuffer_Reset(t *testing.T) {
	r := NewRingBuffer[int](2)
	r.Append(1)
	r.Append(2)
	r.Reset()
	assert.Equal(t, 0, r.Len())
	assert.Empty(t, drain(r))
}

func TestRingBuffer_NonPositiveCapacity(t *testing.T) {
	r := NewRingBuffer[int](0)
	assert.Equal(t, 1, r.Cap())
	r.Append(7)
	r.Append(8) // overwrites
	v, ok := r.Newest()
	require.True(t, ok)
	assert.Equal(t, 8, v)
}

func drain[T any](r *RingBuffer[T]) []T {
	var out []T
	for v := range r.All() {
		out = append(out, v)
	}
	return out
}
