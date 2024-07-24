package output

import (
	"errors"
	"sync"
)

var ErrBufferClosed = errors.New("buffer closed")

type RingBuffer[T any] struct {
	inner        []T
	start, count int
	mu           sync.Mutex
	notEmpty     *sync.Cond
	notFull      *sync.Cond
	closed       bool
}

func NewRingBuffer[T any](capacity uint64) *RingBuffer[T] {
	rb := &RingBuffer[T]{inner: make([]T, capacity)}
	rb.notEmpty = sync.NewCond(&rb.mu)
	rb.notFull = sync.NewCond(&rb.mu)
	return rb
}

func (rb *RingBuffer[T]) Put(item T) error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.closed {
		return ErrBufferClosed
	}

	if rb.count == len(rb.inner) {
		rb.start = (rb.start + 1) % len(rb.inner)
	} else {
		rb.count++
	}

	rb.inner[(rb.start+rb.count-1)%len(rb.inner)] = item
	rb.notEmpty.Signal()
	return nil
}

func (rb *RingBuffer[T]) PutWait(item T) error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	for rb.count == len(rb.inner) && !rb.closed {
		rb.notFull.Wait()
	}

	if rb.closed {
		return ErrBufferClosed
	}

	rb.inner[(rb.start+rb.count)%len(rb.inner)] = item
	rb.count++
	rb.notEmpty.Signal()
	return nil
}

func (rb *RingBuffer[T]) Get() (T, bool, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.closed {
		var zero T
		return zero, false, ErrBufferClosed
	}

	if rb.count == 0 {
		var zero T
		return zero, false, nil
	}

	item := rb.inner[rb.start]
	rb.start = (rb.start + 1) % len(rb.inner)
	rb.count--
	rb.notFull.Signal()

	return item, true, nil
}

func (rb *RingBuffer[T]) GetWait() (T, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	for rb.count == 0 && !rb.closed {
		rb.notEmpty.Wait()
	}

	if rb.closed {
		var zero T
		return zero, ErrBufferClosed
	}

	item := rb.inner[rb.start]
	rb.start = (rb.start + 1) % len(rb.inner)
	rb.count--
	rb.notFull.Signal()

	return item, nil
}

func (rb *RingBuffer[T]) Size() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.count
}

func (rb *RingBuffer[T]) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.start = 0
	rb.count = 0
	rb.notFull.Signal()
}

func (rb *RingBuffer[T]) Close() error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.closed = true
	rb.notEmpty.Broadcast()
	rb.notFull.Broadcast()
	return nil
}
