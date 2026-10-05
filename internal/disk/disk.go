// Package disk serializes reads from the library's storage. On a spinning
// disk, concurrent reads of different files make the head seek back and
// forth between them, which is far slower than reading the files one after
// another; holding the disk while reading keeps the access sequential.
// Faster storage (SSDs, network shares) can serve several reads at once, so
// the number of concurrent holders is configurable, or unlimited.
package disk

import (
	"context"
	"log"
	"sync"
	"time"
)

// Disk limits concurrent reads from the library's storage. At most
// concurrency holders read at a time, or any number with a concurrency of
// 0.
type Disk struct {
	// sem holds a slot per holder; nil when concurrency is unlimited.
	sem chan struct{}
	// exclusive is held for reading by every Do holder and for writing by a
	// DoExclusive holder, so the latter reads alone.
	exclusive sync.RWMutex

	// mu guards the current holders and the waiters, reported by Watch.
	mu      sync.Mutex
	nextID  int
	holders map[int]holder
	waiting int
}

// holder describes a read holding the disk.
type holder struct {
	what  string
	since time.Time
}

// New creates an unheld Disk that allows concurrency simultaneous holders,
// or any number when concurrency is 0.
func New(concurrency int) *Disk {
	d := &Disk{holders: map[int]holder{}}
	if concurrency > 0 {
		d.sem = make(chan struct{}, concurrency)
	}
	return d
}

// Do runs fn while holding the disk and returns how long it waited to
// acquire it. what describes the read for Watch's log lines. When ctx is
// done before a slot of the disk is acquired, fn is not run and ctx's error
// is returned. Waiting for a DoExclusive holder to finish is not
// interrupted by ctx.
func (d *Disk) Do(ctx context.Context, what string, fn func() error) (time.Duration, error) {
	start := time.Now()
	d.mu.Lock()
	d.waiting++
	d.mu.Unlock()

	d.exclusive.RLock()
	release := d.exclusive.RUnlock
	if d.sem != nil {
		select {
		case d.sem <- struct{}{}:
			release = func() {
				<-d.sem
				d.exclusive.RUnlock()
			}
		case <-ctx.Done():
			d.exclusive.RUnlock()
			d.mu.Lock()
			d.waiting--
			d.mu.Unlock()
			return time.Since(start), ctx.Err()
		}
	}
	return d.hold(start, what, release, fn)
}

// DoExclusive is like Do, but waits until no other holder reads and keeps
// them out while fn runs. Waiting is not interrupted by ctx.
func (d *Disk) DoExclusive(ctx context.Context, what string, fn func() error) (time.Duration, error) {
	start := time.Now()
	d.mu.Lock()
	d.waiting++
	d.mu.Unlock()

	d.exclusive.Lock()
	if err := ctx.Err(); err != nil {
		d.exclusive.Unlock()
		d.mu.Lock()
		d.waiting--
		d.mu.Unlock()
		return time.Since(start), err
	}
	return d.hold(start, what, d.exclusive.Unlock, fn)
}

// hold runs fn as a holder that started waiting at start and calls release
// afterwards. It returns how long the holder waited.
func (d *Disk) hold(start time.Time, what string, release func(), fn func() error) (time.Duration, error) {
	waited := time.Since(start)

	d.mu.Lock()
	d.waiting--
	id := d.nextID
	d.nextID++
	d.holders[id] = holder{what: what, since: time.Now()}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.holders, id)
		d.mu.Unlock()
		release()
	}()
	return waited, fn()
}

// Watch logs, every interval, each holder that has held the disk for longer
// than interval, so a long read does not look like a hang. It returns when
// ctx is done.
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
		for _, h := range d.holders {
			if held := time.Since(h.since); held >= interval {
				log.Printf("disk: held for %s by %s (%d waiting)", held.Round(time.Second), h.what, d.waiting)
			}
		}
		d.mu.Unlock()
	}
}
