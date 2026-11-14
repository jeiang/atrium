package mailcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
)

func (o Options) ReplayCommand() *cli.Command {
	return &cli.Command{
		Name: "replay",
		Setup: func(a *cli.Args) cli.Handler {
			account := a.String("account", "", cli.Opt{Help: "replay only this account"})
			limit := a.Int("limit", 0, cli.Opt{Help: "replay at most this many messages"})
			model := a.Bool("model", cli.Opt{Help: "ask the paid classifier at the model stage, on a disposable copy of the database"})
			return func(env *cli.Env) int { return o.replay(env, *account, *limit, *model) }
		},
	}
}

func (o Options) replay(env *cli.Env, account string, limit int, model bool) int {
	if limit < 0 {
		return env.UsageError("argument --limit: must be a whole number of at least 0")
	}
	cfg, err := config.Load(o.env())
	if err != nil {
		return cli.Fail(env, err)
	}
	read, err := mailapp.New(nil, cfg, o.env(), o.Now).WithClassifiers(o.Classifiers).Replay(env.Ctx, mailapp.ReplayInput{Account: account, Limit: limit, Model: model})
	if code, ok := unknownAccount(env, account, err); ok {
		return code
	}
	var source *mailapp.ReplaySourceError
	if errors.As(err, &source) {
		return cli.Fail(env, openError(cfg.DBPath, source.Cause))
	}
	if err != nil {
		return cli.Fail(env, err)
	}
	result := read.Data
	for _, line := range result.Lines {
		data, err := json.Marshal(line)
		if err != nil {
			return cli.Fail(env, err)
		}
		_, _ = fmt.Fprintf(env.Stdout, "%s\n", data)
	}
	for _, reason := range result.Reasons {
		_, _ = fmt.Fprintf(env.Stderr, "not routed: %s (%d messages)\n", reason.Text, reason.Count)
	}
	_, _ = fmt.Fprint(env.Stderr, replaySummary(result.Summary))
	return cli.ExitOK
}

func replaySummary(s mailapp.ReplaySummary) string {
	parts := make([]string, 0, len(mailapp.RouterStages)+1)
	for _, stage := range mailapp.RouterStages {
		parts = append(parts, fmt.Sprintf("%s=%d", stage, s.Stages[stage]))
	}
	if n := s.Stages[mailapp.ReplayStageNone]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s=%d", mailapp.ReplayStageNone, n))
	}
	return fmt.Sprintf("messages replayed: %d\nby stage: %s\ndifferences: %d\n",
		s.Messages, strings.Join(parts, ", "), s.Differ)
}
