package queue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFIFOOrder(t *testing.T) {
	q := New[int]()
	for i := range 30 {
		q.Push(i, 0)
	}
	for range 4 {
		if _, ok := q.Pop(); !ok {
			t.Fatal("unexpected empty queue")
		}
	}
	if got := q.Len(); got != 26 {
		t.Fatalf("Len() = %d, want %d", got, 26)
	}
	want := 4
	for range 26 {
		v, ok := q.Pop()
		if !ok {
			t.Fatal("unexpected empty queue")
		}
		if v != want {
			t.Fatalf("Pop() = %d, want %d", v, want)
		}
		want++
	}
}

func TestPriorityOrder(t *testing.T) {
	q := New[string]()
	q.Push("low", 1)
	q.Push("high", 10)
	q.Push("mid", 5)
	q.Push("negative", -3)
	q.Close()
	for _, want := range []string{"high", "mid", "low", "negative"} {
		got, ok := q.Pop()
		if !ok || got != want {
			t.Fatalf("Pop() = (%q, %v), want (%q, true)", got, ok, want)
		}
	}
}

func TestEqualPriorityStaysFIFO(t *testing.T) {
	q := New[int]()
	for i := range 100 {
		q.Push(i, 5)
	}
	for i := range 100 {
		got, ok := q.Pop()
		if !ok || got != i {
			t.Fatalf("Pop() = (%d, %v), want (%d, true)", got, ok, i)
		}
	}
}

func TestCloseDrainsThenReturnsFalse(t *testing.T) {
	q := New[string]()
	q.Push("a", 0)
	q.Push("b", 0)
	q.Close()
	q.Close() // idempotent
	for _, want := range []string{"a", "b"} {
		got, ok := q.Pop()
		if !ok || got != want {
			t.Fatalf("Pop() = (%q, %v), want (%q, true)", got, ok, want)
		}
	}
	if v, ok := q.Pop(); ok {
		t.Fatalf("Pop() after drain = (%q, true), want (\"\", false)", v)
	}
}

func TestPushAfterClosePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Push after Close did not panic")
		}
	}()
	q := New[int]()
	q.Close()
	q.Push(1, 0)
}

func TestBlockedPopWakesOnClose(t *testing.T) {
	q := New[int]()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := q.Pop(); ok {
			t.Error("Pop() after Close returned ok=true")
		}
	}()
	q.Close()
	<-done
}

func TestConcurrentProducersConsumers(t *testing.T) {
	const (
		producers   = 4
		consumers   = 4
		perProducer = 1000
	)
	q := New[int]()

	producerWG := sync.WaitGroup{}
	var next atomic.Int64
	for range producers {
		producerWG.Go(func() {
			for range perProducer {
				q.Push(int(next.Add(1))-1, 0)
			}
		})
	}

	total := producers * perProducer
	received := make([]int, total)
	var mu sync.Mutex
	var consumerWG sync.WaitGroup
	for range consumers {
		consumerWG.Go(func() {
			for {
				v, ok := q.Pop()
				if !ok {
					return
				}
				mu.Lock()
				received[v]++
				mu.Unlock()
			}
		})
	}

	producerWG.Wait()
	q.Close()
	consumerWG.Wait()

	for v, n := range received {
		if n != 1 {
			t.Fatalf("value %d received %d times, want 1", v, n)
		}
	}
}

func TestBlockingPopReceivesPushedValue(t *testing.T) {
	q := New[int]()
	got := make(chan int)
	go func() {
		v, _ := q.Pop()
		got <- v
	}()
	q.Push(42, 0)
	if v := <-got; v != 42 {
		t.Fatalf("Pop() = %d, want 42", v)
	}
	q.Close()
	if _, ok := q.Pop(); ok {
		t.Fatal("Pop() on closed empty queue returned ok=true")
	}
}

func ExampleQueue() {
	q := New[string]()
	q.Push("hello", 0)
	q.Push("world", 0)
	q.Close()
	for {
		v, ok := q.Pop()
		if !ok {
			break
		}
		fmt.Println(v)
	}
	// Output:
	// hello
	// world
}