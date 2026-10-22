package mailcmd

import (
	"errors"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/backlog"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func (o Options) LiveCommand() *cli.Command {
	return group("live",
		o.leaf("backlog", func(*cli.Args) body { return liveBacklog }),
		o.leaf("rebaseline", func(*cli.Args) body { return rebaseline }),
		o.leaf("pause", func(*cli.Args) body { return setPaused(true) }),
		o.leaf("resume", func(*cli.Args) body { return setPaused(false) }),
		o.leaf("status", func(*cli.Args) body {
			return func(x *Exec) error {
				status, err := x.Live().Status(x.Ctx)
				if err != nil {
					return err
				}
				x.JSON(status)
				return nil
			}
		}),
		o.leaf("deletions", func(*cli.Args) body { return liveDeletions }),
		o.leaf("actions", func(a *cli.Args) body {
			limit := a.Int("limit", 20, cli.Opt{})
			kind := a.String("kind", "", cli.Opt{Choices: live.Kinds[:]})
			return func(x *Exec) error { return listActions(x, *limit, *kind) }
		}),
		o.leaf("resolve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			outcome := a.String("outcome", "", cli.Opt{Choices: []string{proposals.OutcomeUndone, proposals.OutcomeNotUndone}, Required: true, Help: "what the mailbox shows for the blocked undo"})
			return func(x *Exec) error { return resolveAction(x, int64(*id), *outcome) }
		}),
		o.leaf("undo", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			return func(x *Exec) error { return undoAction(x, int64(*id)) }
		}),
	)
}

func backlogDict(r backlog.Result) py.Dict {
	return py.Dict{{Key: "created", Value: r.Created}, {Key: "accounts", Value: r.Accounts}}
}

func liveBacklog(x *Exec) error {
	result, err := backlog.Backlog(x.Ctx, x.DB, x.Now())
	if err != nil {
		var backlogErr backlog.Error
		if errors.As(err, &backlogErr) {
			return failure(cli.ExitUsage, err)
		}
		return err
	}
	x.JSON(backlogDict(result))
	return nil
}

func rebaseline(x *Exec) error {
	return x.Exclusive(func() error {
		if err := run.New(x.DB, x.Cfg, x.Open(), x.opts.Now).SyncAll(x.Ctx); err != nil {
			return err
		}
		baseline, err := x.Live().Rebaseline(x.Ctx)
		if err != nil {
			return err
		}
		refreshed, err := backlog.Refresh(x.Ctx, x.DB, x.Now())
		if err != nil {
			return err
		}
		x.JSON(append(py.Dict{{Key: "baseline", Value: baseline}}, backlogDict(refreshed)...))
		return nil
	})
}

func setPaused(paused bool) body {
	return func(x *Exec) error {
		l := x.Live()
		if err := l.SetPaused(paused); err != nil {
			return err
		}
		x.JSON(py.Dict{{Key: "paused", Value: l.Paused()}})
		return nil
	}
}

func liveDeletions(x *Exec) error {
	return x.Exclusive(func() error {
		accounts, err := run.SelectAccounts(x.Cfg, "")
		if err != nil {
			return err
		}
		syncer := x.Syncer()
		out := py.Dict{}
		for _, account := range accounts {
			done, err := applier.New(x.Live(), x.Open(), syncer).DeletionPass(x.Ctx, account, true)
			if err != nil {
				return err
			}
			out = out.Set(account, py.Dict{
				{Key: "ran", Value: done.Ran}, {Key: "trashed", Value: done.Kind(live.Trash)},
				{Key: "skipped", Value: done.Skipped}, {Key: "failed", Value: done.Failed},
			})
		}
		x.JSON(out)
		return nil
	})
}

func listActions(x *Exec, limit int, kind string) error {
	actions, err := live.List(x.Ctx, x.DB.Read, "", kind, limit, 0)
	if err != nil {
		return err
	}
	out := make([]any, len(actions))
	for i, a := range actions {
		out[i] = py.Dict{
			{Key: "id", Value: a.ID}, {Key: "ts", Value: a.TS}, {Key: "account", Value: a.Account}, {Key: "kind", Value: a.Kind},
			{Key: "status", Value: a.Status}, {Key: "src", Value: nullable(a.Src)}, {Key: "dest", Value: nullable(a.Dest)},
			{Key: "subject", Value: nullable(a.Subject)}, {Key: "error", Value: nullable(a.Error)}, {Key: "undoable", Value: a.Undoable},
		}
	}
	x.JSON(out)
	return nil
}

func undoAction(x *Exec, id int64) error {
	result, err := x.Live().Undo(x.Ctx, id)
	if err != nil {
		var undoErr live.UndoError
		if errors.As(err, &undoErr) {
			return failure(cli.ExitError, err)
		}
		return err
	}
	x.JSON(py.Dict{
		{Key: "id", Value: result.ID}, {Key: "undone", Value: result.Undone}, {Key: "detail", Value: result.Detail},
		{Key: "sync_error", Value: nullable(result.SyncError)},
	})
	return nil
}

func resolveAction(x *Exec, id int64, outcome string) error {
	result, err := x.Live().Resolve(x.Ctx, id, outcome, operator)
	if err != nil {
		var undoErr live.UndoError
		if errors.As(err, &undoErr) {
			return failure(cli.ExitError, err)
		}
		return err
	}
	x.JSON(result)
	return nil
}

func nullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}
