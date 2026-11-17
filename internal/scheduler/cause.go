package scheduler

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/db"
)

type causeKey struct{}

func withCause(ctx context.Context) (context.Context, *atomic.Bool) {
	var reported atomic.Bool
	return context.WithValue(ctx, causeKey{}, &reported), &reported
}

func CauseReporter(ctx context.Context) func() {
	reported, ok := ctx.Value(causeKey{}).(*atomic.Bool)
	if !ok {
		return nil
	}
	return func() { reported.Store(true) }
}

type causeRun struct {
	d   *db.DB
	job int64
}

var causes sync.Map

func CauseReported(d *db.DB, job, run int64) bool {
	got, ok := causes.Load(causeRun{d: d, job: job})
	return ok && got.(int64) == run
}
