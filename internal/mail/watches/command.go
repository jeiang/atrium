package watches

import (
	"context"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/refreshcmd"
	"github.com/jeiang/atrium/internal/py"
)

func Command(clock func() float64) *cli.Command {
	o := Options{Clock: clock}
	return refreshcmd.Command("watches", func(ctx context.Context, d *db.DB, _ config.Config) py.Dict {
		return Refresh(ctx, d, o.clock(), o)
	})
}
