package mailcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/backlog"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

func (o Options) LiveCommand() *cli.Command {
	return group("live",
		o.leaf("backlog", func(*cli.Args) body { return liveBacklog }),
		o.leaf("rebaseline", func(*cli.Args) body { return rebaseline }),
		o.leaf("pause", func(*cli.Args) body { return setPaused(true) }),
		o.leaf("resume", func(*cli.Args) body { return setPaused(false) }),
		o.leaf("status", func(*cli.Args) body {
			return func(x *exec) error {
				status, err := x.Live.Status(x.Ctx)
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
			return func(x *exec) error { return listActions(x, *limit, *kind) }
		}),
		o.leaf("resolve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			outcome := a.String("outcome", "", cli.Opt{Choices: []string{proposals.OutcomeUndone, proposals.OutcomeNotUndone, proposals.OutcomeApplied, proposals.OutcomeNotApplied}, Required: true, Help: "what the mailbox shows for the blocked write"})
			return func(x *exec) error { return resolveAction(x, int64(*id), *outcome) }
		}),
		o.leaf("undo", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			return func(x *exec) error { return undoAction(x, int64(*id)) }
		}),
	)
}

func liveBacklog(x *exec) error {
	result, err := backlog.Backlog(x.Ctx, x.DB, x.Now())
	if err != nil {
		var backlogErr backlog.Error
		if errors.As(err, &backlogErr) {
			return failure(cli.ExitUsage, err)
		}
		return err
	}
	x.JSON(result)
	return nil
}

func rebaseline(x *exec) error {
	return x.Exclusive(func() error {
		if err := x.SyncAll(x.Ctx); err != nil {
			return err
		}
		baseline, err := x.Live.Rebaseline(x.Ctx)
		if err != nil {
			return err
		}
		refreshed, err := backlog.Refresh(x.Ctx, x.DB, x.Now())
		if err != nil {
			return err
		}
		x.JSON(rebaselineResult{Baseline: baseline, Created: refreshed.Created, Accounts: refreshed.Accounts})
		return nil
	})
}

func setPaused(paused bool) body {
	return func(x *exec) error {
		l := x.Live
		if err := l.SetPaused(paused); err != nil {
			return err
		}
		if err := notify.PauseChanged(x.Ctx, x.DB, x.opts.env(), paused); err != nil {
			_, _ = fmt.Fprintln(x.Stderr, "warning: pause notice failed:", err)
		}
		x.JSON(map[string]any{"paused": l.Paused()})
		return nil
	}
}

func liveDeletions(x *exec) error {
	return x.Exclusive(func() error {
		accounts, err := run.SelectAccounts(x.Ctx, "")
		if err != nil {
			return err
		}
		syncer := x.Syncer
		out := accountResults{}
		for _, account := range accounts {
			done, err := applier.New(x.Live, x.Open, syncer).DeletionPass(x.Ctx, account, true)
			if err != nil {
				return err
			}
			out = append(out, accountResult{name: account, result: deletionResult{Ran: done.Ran, Trashed: done.Kind(live.Trash), Skipped: done.Skipped, Failed: done.Failed}})
		}
		x.JSON(out)
		return nil
	})
}

func listActions(x *exec, limit int, kind string) error {
	actions, err := x.Live.List(x.Ctx, "", kind, limit, 0)
	if err != nil {
		return err
	}
	out := make([]actionJSON, len(actions))
	for i, a := range actions {
		out[i] = actionJSON{ID: a.ID, TS: a.TS, Account: a.Account, Kind: a.Kind, Status: a.Status,
			Src: nullable(a.Src), Dest: nullable(a.Dest), Subject: nullable(a.Subject), Error: nullable(a.Error), Undoable: a.Undoable,
			Blocked: a.Blocked, UndoBlocked: a.UndoBlocked, Resolved: a.Resolved, EffectiveOutcome: a.EffectiveOutcome}
	}
	x.JSON(out)
	return nil
}

func undoAction(x *exec, id int64) error {
	done, err := reviewapp.New(reviewapp.Deps{DB: x.DB, Live: x.Live}).Undo(x.Ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.MailTarget, ID: id}})
	if e := (*application.Error)(nil); errors.As(err, &e) && e.Code == application.NotFound {
		return fmt.Errorf("unknown action %d", id)
	}
	if err != nil {
		return err
	}
	result := done.Data.Mail
	x.JSON(undoJSON{ID: result.ID, Undone: result.Undone, Detail: result.Detail, SyncError: result.SyncError})
	return nil
}

func resolveAction(x *exec, id int64, outcome string) error {
	done, err := reviewapp.New(reviewapp.Deps{DB: x.DB, Live: x.Live}).Resolve(x.Ctx, reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.MailTarget, ID: id}, Outcome: outcome})
	if err != nil {
		return err
	}
	x.JSON(map[string]any{"id": done.Data.Mail.ID, "outcome": done.Data.Mail.Outcome})
	return nil
}

type rebaselineResult struct {
	Baseline any              `json:"baseline"`
	Created  []int64          `json:"created"`
	Accounts backlog.Accounts `json:"accounts"`
}

type accountResult struct {
	name   string
	result deletionResult
}

type accountResults []accountResult

func (r accountResults) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, entry := range r {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(entry.name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(entry.result)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type deletionResult struct {
	Ran     bool `json:"ran"`
	Trashed int  `json:"trashed"`
	Skipped int  `json:"skipped"`
	Failed  int  `json:"failed"`
}

type actionJSON struct {
	ID               int64   `json:"id"`
	TS               float64 `json:"ts"`
	Account          string  `json:"account"`
	Kind             string  `json:"kind"`
	Status           string  `json:"status"`
	Src              any     `json:"src"`
	Dest             any     `json:"dest"`
	Subject          any     `json:"subject"`
	Error            any     `json:"error"`
	Undoable         bool    `json:"undoable"`
	Blocked          bool    `json:"blocked,omitempty"`
	UndoBlocked      bool    `json:"undo_blocked,omitempty"`
	Resolved         bool    `json:"resolved,omitempty"`
	EffectiveOutcome string  `json:"effective_outcome,omitempty"`
}

type undoJSON struct {
	ID        int64   `json:"id"`
	Undone    bool    `json:"undone"`
	Detail    any     `json:"detail"`
	SyncError *string `json:"sync_error"`
}

func nullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}
