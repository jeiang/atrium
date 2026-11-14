package mailcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
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
				status, err := x.opts.Application(x.Pass).LiveStatus(x.Ctx, struct{}{})
				if err != nil {
					return err
				}
				x.JSON(status.Data)
				return nil
			}
		}),
		o.leaf("deletions", func(*cli.Args) body { return liveDeletions }),
		o.leaf("actions", func(a *cli.Args) body {
			limit := a.Int("limit", 20, cli.Opt{})
			kind := a.String("kind", "", cli.Opt{Choices: mailapp.LiveKinds[:]})
			return func(x *exec) error { return listActions(x, *limit, *kind) }
		}),
		o.leaf("resolve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			outcome := a.String("outcome", "", cli.Opt{Choices: []string{proposalsapp.OutcomeUndone, proposalsapp.OutcomeNotUndone, proposalsapp.OutcomeApplied, proposalsapp.OutcomeNotApplied}, Required: true, Help: "what the mailbox shows for the blocked write"})
			return func(x *exec) error { return resolveAction(x, int64(*id), *outcome) }
		}),
		o.leaf("undo", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			return func(x *exec) error { return undoAction(x, int64(*id)) }
		}),
	)
}

func liveBacklog(x *exec) error {
	done, err := x.opts.Application(x.Pass).WithClock(x.Now).Backlog(x.Ctx, struct{}{})
	result := done.Data
	if err != nil {
		var backlogErr mailapp.BacklogError
		if errors.As(err, &backlogErr) {
			return failure(cli.ExitUsage, err)
		}
		return err
	}
	x.JSON(result)
	return nil
}

func rebaseline(x *exec) error {
	return x.PassLocked(func() error {
		done, err := x.opts.Application(x.Pass).WithClock(x.Now).WithSync(x.SyncAll).Rebaseline(x.Ctx, struct{}{})
		if err != nil {
			return err
		}
		x.JSON(done.Data)
		return nil
	})
}

func setPaused(paused bool) body {
	return func(x *exec) error {
		svc := x.opts.Application(x.Pass)
		var done application.Change[mailapp.PauseResult]
		var err error
		if paused {
			done, err = svc.Pause(x.Ctx, mailapp.PauseInput{})
		} else {
			done, err = svc.Resume(x.Ctx, mailapp.PauseInput{})
		}
		if err != nil && !mailapp.Committed(err) {
			return err
		}
		if err := done.Data.NoticeError; err != nil {
			_, _ = fmt.Fprintln(x.Stderr, "warning: pause notice failed:", err)
		}
		x.JSON(done.Data)

		return nil
	}
}

func liveDeletions(x *exec) error {
	return x.PassLocked(func() error {
		accounts, err := mailapp.SelectAccounts(x.Ctx)
		if err != nil {
			return err
		}
		done, err := x.opts.Application(x.Pass).WithOpen(x.Open).WithSyncer(x.Syncer).ApplyDeletions(x.Ctx, mailapp.ApplyDeletionsInput{Accounts: accounts})
		if err != nil {
			return err
		}
		out := accountResults{}
		for _, a := range done.Data.Accounts {
			out = append(out, accountResult{name: a.Account, result: deletionResult{Ran: a.Ran, Trashed: a.Trashed, Skipped: a.Skipped, Failed: a.Failed}})
		}
		x.JSON(out)
		return nil
	})
}

func listActions(x *exec, limit int, kind string) error {
	read, err := x.opts.Application(x.Pass).LiveActions(x.Ctx, mailapp.LiveActionsInput{Limit: limit, Kind: kind})
	actions := read.Data
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
