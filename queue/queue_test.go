package queue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFIFOWithWrapAndGrow(t *testing.T) {
	q := New[int]()
	for i := range 10 {
		q.Push(i)
	}
	for range 4 {
		if _, ok := q.Pop(); !ok {
			t.Fatal("unexpected empty queue")
		}
	}
	// Push past the current capacity with head != 0 so both head and
	// tail indices wrap across the buffer end during grow and pops.
	for i := 10; i < 30; i++ {
		q.Push(i)
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

func TestCloseDrainsThenReturnsFalse(t *testing.T) {
	q := New[string]()
	q.Push("a")
	q.Push("b")
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
	q.Push(1)
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
				q.Push(int(next.Add(1)) - 1)
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
	q.Push(42)
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
	q.Push("hello")
	q.Push("world")
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
