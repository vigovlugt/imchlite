package queue

import (
	"container/heap"
	"sync"
)

// priorityQueueItem is a value queued with a priority. seq is a
// monotonically increasing sequence number used to keep equal-priority
// items in FIFO order.
type priorityQueueItem[T any] struct {
	value    T
	priority int
	seq      uint64
}

// priorityHeap is a max-heap ordered by priority, breaking ties by seq
// (lower seq first) so that equal-priority items stay FIFO.
type priorityHeap[T any] []priorityQueueItem[T]

func (h priorityHeap[T]) Len() int { return len(h) }

func (h priorityHeap[T]) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority > h[j].priority
	}
	return h[i].seq < h[j].seq
}

func (h priorityHeap[T]) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *priorityHeap[T]) Push(x any) {
	*h = append(*h, x.(priorityQueueItem[T]))
}

func (h *priorityHeap[T]) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	var zero priorityQueueItem[T]
	old[n-1] = zero // release references for the GC
	*h = old[:n-1]
	return item
}

type Queue[T any] struct {
	mu       sync.Mutex
	notEmpty *sync.Cond
	items    priorityHeap[T]
	nextSeq  uint64
	closed   bool
}

func New[T any]() *Queue[T] {
	q := &Queue[T]{}
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

// Push enqueues v with the given priority; higher priorities are popped
// first. Items of equal priority are popped in the order they were pushed.
func (q *Queue[T]) Push(v T, priority int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		panic("queue: push on closed queue")
	}
	heap.Push(&q.items, priorityQueueItem[T]{value: v, priority: priority, seq: q.nextSeq})
	q.nextSeq++
	q.notEmpty.Signal()
}

func (q *Queue[T]) Pop() (v T, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.items.Len() == 0 && !q.closed {
		q.notEmpty.Wait()
	}
	if q.items.Len() == 0 {
		var zero T
		return zero, false
	}
	item := heap.Pop(&q.items).(priorityQueueItem[T])
	return item.value, true
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
	return q.items.Len()
}