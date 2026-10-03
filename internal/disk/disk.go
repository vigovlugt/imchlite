// Package disk serializes reads from the library's storage. On a spinning
// disk, concurrent reads of different files make the head seek back and
// forth between them, which is far slower than reading the files one after
// another; holding the disk while reading keeps the access sequential.
package disk

import (
	"context"
	"time"
)

// Disk is a lock around reads from the library's storage. At most one
// holder reads at a time.
type Disk struct {
	sem chan struct{}
}

// New creates an unheld Disk.
func New() *Disk {
	return &Disk{sem: make(chan struct{}, 1)}
}

// Do runs fn while holding the disk and returns how long it waited to
// acquire it. When ctx is done before the disk is acquired, fn is not run
// and ctx's error is returned.
func (d *Disk) Do(ctx context.Context, fn func() error) (time.Duration, error) {
	start := time.Now()
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return time.Since(start), ctx.Err()
	}
	waited := time.Since(start)
	defer func() { <-d.sem }()
	return waited, fn()
}
