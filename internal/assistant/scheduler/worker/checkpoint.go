package worker

import (
	"context"

	"github.com/jeiang/atrium/internal/logx"
)

// idle reports whether no claim of this worker is executing and no agent run is running in any process.
func (w *Worker) idle(ctx context.Context) bool {
	if w.active.Load() > 0 {
		return false
	}
	var running int
	if err := w.Service.DB.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE state = 'running'").Scan(&running); err != nil {
		return false
	}
	return running == 0
}

// checkpoint runs PRAGMA wal_checkpoint(TRUNCATE) at an idle point (nothing running since the last write),
// so the WAL does not grow while the always-on web process holds a reader. A busy checkpoint is retried at
// the next idle point.
func (w *Worker) checkpoint(ctx context.Context) {
	if ctx.Err() != nil || !w.dirty.Load() || !w.idle(ctx) {
		return
	}
	var busy, logFrames, moved int
	if err := w.Service.DB.Write.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &moved); err != nil {
		log.Error("wal checkpoint failed", logx.Err, err)
		return
	}
	if busy != 0 {
		log.Debug("wal checkpoint busy; retrying at the next idle point")
		return
	}
	w.dirty.Store(false)
}
