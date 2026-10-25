package loginlink

import (
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func Command() *cli.Command {
	return &cli.Command{
		Name: "login-link",
		Help: "print a single-use administrator sign-in link",
		Setup: func(a *cli.Args) cli.Handler {
			minutes := a.Int("minutes", int(DefaultTTL/time.Minute), cli.Opt{Help: "minutes the link stays valid"})
			return func(e *cli.Env) int {
				ttl := time.Duration(*minutes) * time.Minute
				if ttl <= 0 || ttl > MaxTTL {
					return e.UsageError("--minutes must be between 1 and %d", int(MaxTTL/time.Minute))
				}
				cfg, err := config.Load(config.OS)
				if err != nil {
					return cli.Fail(e, err)
				}
				d, err := db.Connect(cfg.DBPath)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if err := config.Migrate(e.Ctx, d, cfg); err != nil {
					return cli.Fail(e, err)
				}
				base := config.PublicURL(config.OS)
				if base == "" {
					_, _ = fmt.Fprintf(e.Stderr, "error: %s is not set; the link needs the public URL\n", config.KeyPublicURL)
					return cli.ExitError
				}
				token, err := New(d).Issue(e.Ctx, ttl)
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintf(e.Stdout, "%s%s#%s\n", strings.TrimRight(base, "/"), Path, token)
				return cli.ExitOK
			}
		},
	}
}
