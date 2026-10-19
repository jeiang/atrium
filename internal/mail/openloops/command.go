package openloops

import (
	"context"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/refreshcmd"
	"github.com/jeiang/atrium/internal/py"
)

// Command is `loops refresh`: one refresh pass under the database's exclusive lock, printed as JSON. The root
// command adds it; clock is the pass's clock in seconds (the wall clock when nil).
func Command(clock func() float64) *cli.Command {
	if clock == nil {
		clock = wallClock
	}
	return refreshcmd.Command("loops", func(ctx context.Context, d *db.DB, cfg config.Config) py.Dict {
		return Refresh(ctx, d, cfg, clock(), Options{Clock: clock})
	})
}
