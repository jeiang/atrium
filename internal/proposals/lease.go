package proposals

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/logx"
)

const RenewSeconds = 30

var leaseLog = logx.Logger("atrium.proposals.lease")

func KeepAlive(x Execer, now func() float64, query string, args ...any) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(RenewSeconds * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if _, err := x.ExecContext(context.Background(), query, append([]any{now()}, args...)...); err != nil {
					leaseLog.Warn("in-flight lease not renewed", logx.Err, err.Error())
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func KeepActionAlive(x Execer, now func() float64, id int64) (stop func()) {
	return KeepAlive(x, now, "UPDATE proposal_actions SET ts = ? WHERE id = ? AND ok = 0 AND error = ?", id, InFlightText)
}
