package mailcmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/mail/replay"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/settings"
)

func (o Options) ReplayCommand() *cli.Command {
	return &cli.Command{
		Name: "replay",
		Setup: func(a *cli.Args) cli.Handler {
			account := a.String("account", "", cli.Opt{Help: "replay only this account"})
			limit := a.Int("limit", 0, cli.Opt{Help: "replay at most this many messages"})
			return func(env *cli.Env) int { return o.replay(env, *account, *limit) }
		},
	}
}

func (o Options) replay(env *cli.Env, account string, limit int) int {
	if limit < 0 {
		return env.UsageError("argument --limit: must be a whole number of at least 0")
	}
	cfg, err := config.Load(o.env())
	if err != nil {
		return cli.Fail(env, err)
	}
	key, err := dbkey.Load(o.env(), settings.From(env.Ctx))
	if err != nil {
		return cli.Fail(env, err)
	}
	q, err := db.OpenSource(cfg.DBPath, key)
	if err != nil {
		return cli.Fail(env, openError(cfg.DBPath, err))
	}
	defer func() { _ = q.Close() }()
	state := config.From(env.Ctx)
	if err := state.LoadStoredAccounts(env.Ctx, q); err != nil {
		return cli.Fail(env, err)
	}
	if account != "" {
		if _, ok := state.Account(account); !ok {
			names := make([]string, 0, len(state.Accounts()))
			for _, a := range state.Accounts() {
				names = append(names, strconv.Quote(a.Name))
			}
			slices.Sort(names)
			return env.UsageError("argument --account: invalid choice %q (choose from %s)", account, strings.Join(names, ", "))
		}
	}
	result, err := replay.Run(env.Ctx, q, replay.Options{Account: account, Limit: limit})
	if err != nil {
		return cli.Fail(env, err)
	}
	for _, line := range result.Lines {
		data, err := json.Marshal(line)
		if err != nil {
			return cli.Fail(env, err)
		}
		_, _ = fmt.Fprintf(env.Stdout, "%s\n", data)
	}
	_, _ = fmt.Fprint(env.Stderr, replaySummary(result.Summary))
	return cli.ExitOK
}

func replaySummary(s replay.Summary) string {
	parts := make([]string, 0, len(router.Stages)+1)
	for _, stage := range router.Stages {
		parts = append(parts, fmt.Sprintf("%s=%d", stage, s.Stages[stage]))
	}
	if n := s.Stages[replay.StageNone]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s=%d", replay.StageNone, n))
	}
	return fmt.Sprintf("messages replayed: %d\nby stage: %s\ndifferences: %d\n",
		s.Messages, strings.Join(parts, ", "), s.Differ)
}
