package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

type Running struct {
	JobID int64
	RunID int64
}

func (e *Running) Error() string {
	held := e.Unwrap().Error()
	if e.RunID == 0 {
		return held
	}
	return fmt.Sprintf("%s (run %d)", held, e.RunID)
}

func (e *Running) Unwrap() error { return &db.LockBusy{Resource: jobResource(e.JobID)} }

type fenceKey struct {
	path string
	job  int64
}

var fence = struct {
	sync.Mutex
	runs map[fenceKey]int64
}{runs: map[fenceKey]int64{}}

func (s *Scheduler) fenceKey(jobID int64) fenceKey {
	return fenceKey{path: s.deps.DBPath, job: jobID}
}

func (s *Scheduler) enter(jobID int64) *Running {
	key := s.fenceKey(jobID)
	fence.Lock()
	defer fence.Unlock()
	if run, busy := fence.runs[key]; busy {
		return &Running{JobID: jobID, RunID: run}
	}
	fence.runs[key] = 0
	return nil
}

func (s *Scheduler) enterWait(ctx context.Context, jobID int64, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		busy := s.enter(jobID)
		if busy == nil {
			return nil
		}
		if wait > 0 && !time.Now().Before(deadline) {
			return busy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *Scheduler) hold(jobID, runID int64) {
	fence.Lock()
	fence.runs[s.fenceKey(jobID)] = runID
	fence.Unlock()
}

func (s *Scheduler) leave(jobID int64) {
	fence.Lock()
	delete(fence.runs, s.fenceKey(jobID))
	fence.Unlock()
}
