package readpaths_test

import (
	"context"
	"path/filepath"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

// readClock is the clock make_db.py's read paths ran at (t0 + 3 days); the birthdays' today is its date.
const readClock = 1_792_339_200.0

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "openloops.ListLoops",
		Golden: "openloops.list_loops",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return openloops.ListLoops(ctx, d.Read, readClock, "", openloops.DefaultListLimit, false)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "openloops.ListCommitments",
		Golden: "openloops.list_commitments",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return openloops.ListCommitments(ctx, d.Read, "", openloops.DefaultListLimit)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "watches.ListPackages",
		Golden: "watches.list_packages",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return watches.ListPackages(ctx, d.Read, goldenEnv(), readClock, false, true)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "birthdays.UpcomingFor",
		Golden: "birthdays.upcoming_for",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			cfg := config.Config{ContactsDir: filepath.Join(golden.Dir(), "state", "contacts")}
			items, _, err := birthdays.UpcomingFor(ctx, d.Read, cfg, birthdays.Date(2026, 10, 18), 30)
			return items, err
		},
	})
}
