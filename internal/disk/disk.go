// Package disk serializes reads from the library's storage. On a spinning
// disk, concurrent reads of different files make the head seek back and
// forth between them, which is far slower than reading the files one after
// another; holding the disk while reading keeps the access sequential.
package disk

import (
	"context"
	"log"
	"sync"
	"time"
)

// Disk is a lock around reads from the library's storage. At most one
// holder reads at a time.
type Disk struct {
	sem chan struct{}

	// mu guards the description of the current holder and the waiters,
	// reported by Watch.
	mu      sync.Mutex
	holder  string
	since   time.Time
	waiting int
}

// New creates an unheld Disk.
func New() *Disk {
	return &Disk{sem: make(chan struct{}, 1)}
}

// Do runs fn while holding the disk and returns how long it waited to
// acquire it. what describes the read for Watch's log lines. When ctx is
// done before the disk is acquired, fn is not run and ctx's error is
// returned.
func (d *Disk) Do(ctx context.Context, what string, fn func() error) (time.Duration, error) {
	start := time.Now()
	d.mu.Lock()
	d.waiting++
	d.mu.Unlock()
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		d.mu.Lock()
		d.waiting--
		d.mu.Unlock()
		return time.Since(start), ctx.Err()
	}
	waited := time.Since(start)

	d.mu.Lock()
	d.waiting--
	d.holder = what
	d.since = time.Now()
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.holder = ""
		d.mu.Unlock()
		<-d.sem
	}()
	return waited, fn()
}

// Watch logs the disk's holder every interval while it has held the disk
// for longer than interval, so a long read does not look like a hang. It
// returns when ctx is done.
func (d *Disk) Watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		d.mu.Lock()
		holder, held, waiting := d.holder, time.Since(d.since), d.waiting
		d.mu.Unlock()
		if holder != "" && held >= interval {
			log.Printf("disk: held for %s by %s (%d waiting)", held.Round(time.Second), holder, waiting)
		}
	}
}
