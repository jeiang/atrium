package watches

import (
	"context"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/refreshcmd"
	"github.com/jeiang/atrium/internal/py"
)

// Command is `watches refresh`: one refresh pass under the database's exclusive lock, printed as JSON. The root
// command adds it.
func Command() *cli.Command {
	return refreshcmd.Command("watches", func(ctx context.Context, d *db.DB, cfg config.Config) py.Dict {
		return Refresh(ctx, d, cfg, ScanLimit, Options{}.clock(), Options{})
	})
}
