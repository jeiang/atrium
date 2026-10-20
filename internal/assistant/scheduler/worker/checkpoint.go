package worker

import (
	"context"

	"github.com/jeiang/atrium/internal/logx"
)

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
