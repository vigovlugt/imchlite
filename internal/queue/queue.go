package queue

import "sync"

type Queue[T any] struct {
	mu       sync.Mutex
	notEmpty *sync.Cond
	items    []T // ring buffer; len may be 0 before the first push
	head     int // index of the oldest element
	count    int // number of elements stored
	closed   bool
}

func New[T any]() *Queue[T] {
	q := &Queue[T]{}
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

func (q *Queue[T]) Push(v T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		panic("queue: push on closed queue")
	}
	if q.count == len(q.items) {
		q.grow()
	}
	tail := (q.head + q.count) % len(q.items)
	q.items[tail] = v
	q.count++
	q.notEmpty.Signal()
}

func (q *Queue[T]) Pop() (v T, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.count == 0 && !q.closed {
		q.notEmpty.Wait()
	}
	if q.count == 0 {
		var zero T
		return zero, false
	}
	v = q.items[q.head]
	var zero T
	q.items[q.head] = zero // release references for the GC
	q.head = (q.head + 1) % len(q.items)
	q.count--
	return v, true
}

func (q *Queue[T]) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.notEmpty.Broadcast()
}

func (q *Queue[T]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}

func (q *Queue[T]) grow() {
	newCap := max(2*len(q.items), 8)
	items := make([]T, newCap)
	n := copy(items, q.items[q.head:])
	if n < q.count {
		copy(items[n:], q.items[:q.count-n])
	}
	q.items = items
	q.head = 0
}
