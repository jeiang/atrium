package openloops

import (
	"context"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/refreshcmd"
	"github.com/jeiang/atrium/internal/py"
)

func Command(clock func() float64) *cli.Command {
	if clock == nil {
		clock = wallClock
	}
	return refreshcmd.Command("loops", func(ctx context.Context, d *db.DB, cfg config.Config) py.Dict {
		return Refresh(ctx, d, cfg, clock(), Options{Clock: clock})
	})
}
