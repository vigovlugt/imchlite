package tasks

import (
	"sync/atomic"
	"time"
)

// TaskState tracks live progress of the background tasks (the index walk
// and the file processing it enqueues) so the api can report on it.
type TaskState struct {
	startedAt  time.Time
	discovered atomic.Int64
	processed  atomic.Int64
	// skipped counts files that needed no processing in this run because
	// they were already processed by a previous run.
	skipped   atomic.Int64
	errored   atomic.Int64
	completed atomic.Bool
	failed    atomic.Bool
	errMsg    atomic.Pointer[string]
}

// NewTaskState creates a fresh, empty task state.
func NewTaskState() *TaskState {
	return &TaskState{startedAt: time.Now()}
}

// Complete marks the index run as finished.
func (s *TaskState) Complete() {
	s.completed.Store(true)
}

func (s *TaskState) fail(err error) {
	msg := err.Error()
	s.errMsg.Store(&msg)
	s.failed.Store(true)
}

// IndexStatus is the serializable snapshot of the indexer's progress.
type IndexStatus struct {
	StartedAt  time.Time `json:"startedAt"`
	Discovered int64     `json:"discovered"`
	Processed  int64     `json:"processed"`
	Errored    int64     `json:"errored"`
	Phase      string    `json:"phase"`
	ETASeconds int64     `json:"etaSeconds,omitempty"`
	Completed  bool      `json:"completed"`
	Failed     bool      `json:"failed"`
	Error      string    `json:"error,omitempty"`
}

// Status returns the current progress snapshot.
func (s *TaskState) Status() IndexStatus {
	status := IndexStatus{
		StartedAt:  s.startedAt,
		Discovered: s.discovered.Load(),
		Processed:  s.processed.Load(),
		Errored:    s.errored.Load(),
		Completed:  s.completed.Load(),
		Failed:     s.failed.Load(),
	}

	switch {
	case s.failed.Load():
		status.Phase = "failed"
	case !s.completed.Load():
		status.Phase = "indexing"
	case s.processed.Load()+s.errored.Load() < s.discovered.Load():
		status.Phase = "processing"
	default:
		status.Phase = "completed"
	}

	// The ETA is based only on work performed in this run: skipped files
	// were counted at walk speed (near-zero time), so including them in
	// the rate would understate the remaining time.
	done := status.Processed - int64(s.skipped.Load()) + status.Errored
	remaining := status.Discovered - status.Processed - status.Errored
	if status.Phase != "failed" && done > 0 && remaining > 0 {
		elapsed := time.Since(s.startedAt)
		status.ETASeconds = int64(elapsed / time.Duration(done) * time.Duration(remaining) / time.Second)
	}

	if msg := s.errMsg.Load(); msg != nil {
		status.Error = *msg
	}
	return status
}
