package applier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/safety"
)

type snoozeRow struct {
	ID          int64
	MessageID   int64
	IMAPName    string
	UIDValidity uint32
	UID         uint32
	DueTS       float64
}

// ApplySnoozes returns the due snoozes for account. In shadow mode it records
// the would-be return and writes nothing; while paused it leaves the row
// pending so the return happens after resume.
func (a *Applier) ApplySnoozes(ctx context.Context, account string) (Outcome, error) {
	var out Outcome
	rows, err := a.Live.DB.Read.QueryContext(ctx, "SELECT id, message_id, imap_name, uidvalidity, uid, due_ts FROM snoozes "+
		"WHERE account = ? AND state = 'pending' AND due_ts <= ? ORDER BY due_ts, id", account, a.now())
	if err != nil {
		return out, err
	}
	var due []snoozeRow
	for rows.Next() {
		var s snoozeRow
		if err := rows.Scan(&s.ID, &s.MessageID, &s.IMAPName, &s.UIDValidity, &s.UID, &s.DueTS); err != nil {
			_ = rows.Close()
			return out, err
		}
		due = append(due, s)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(due) == 0 {
		return out, nil
	}
	mode, err := mail.Mode(ctx, a.Live.DB.Read)
	if err != nil {
		return out, err
	}
	pause := a.Live.Cfg.PausePath()
	if mode == config.Live && safety.IsPaused(pause) {
		log.Info(account + ": paused, snooze returns skipped")
		return out, nil
	}
	out.Ran = true
	ctx = context.WithoutCancel(ctx)
	limit, err := a.cap(ctx)
	if err != nil {
		return out, err
	}
	if len(due) > limit {
		log.Warn(fmt.Sprintf("%s: write cap reached, deferring %d snooze returns", account, len(due)-limit))
		due = due[:limit]
	}
	for _, s := range due {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		snaps, err := mailops.Snapshots(ctx, a.Live.DB.Read, []int64{s.MessageID})
		if err != nil {
			return out, err
		}
		if len(snaps) == 0 {
			if err := a.finishSnooze(ctx, s.ID, "failed", "the message is gone"); err != nil {
				return out, err
			}
			out.Failed++
			continue
		}
		digest := snaps[0].Digest
		inbox := config.Inbox
		if mode != config.Live {
			if err := a.Live.Skip(ctx, account, s.MessageID, digest, live.SnoozeReturn, &s.IMAPName, &inbox, "shadow mode: would return the snooze", nil); err != nil {
				return out, err
			}
			out.Skipped++
			continue
		}
		auto := safety.AutomaticWrites(ctx, pause)
		before := map[string]any{"imap_name": s.IMAPName, "uidvalidity": s.UIDValidity, "uid": s.UID}
		ticket, ok, err := a.Live.Begin(auto, account, live.Target{
			MessageID: s.MessageID, IMAPName: s.IMAPName, UIDValidity: s.UIDValidity, UID: s.UID,
		}, digest, live.SnoozeReturn, &s.IMAPName, &inbox, before, nil)
		if err != nil {
			return out, err
		}
		if !ok {
			if err := a.finishSnooze(ctx, s.ID, "failed", "a return for this message is already in flight"); err != nil {
				return out, err
			}
			out.Failed++
			continue
		}
		err = mailops.Session(auto, a.Open, a.Live.DB.Read, account, func(client mail.Client) error {
			return a.returnSnooze(auto, client, account, s)
		})
		if err != nil {
			var interrupted *mail.WriteInterrupted
			if errors.As(err, &interrupted) {
				if uerr := a.settleSnooze(ctx, ticket, ledger.Result{State: ledger.Unknown, Error: err.Error()}, s.ID, "failed", err.Error()); uerr != nil {
					return out, uerr
				}
			} else if ferr := a.settleSnooze(ctx, ticket, ledger.Result{State: ledger.Failed, Error: err.Error()}, s.ID, "failed", err.Error()); ferr != nil {
				return out, ferr
			}
			out.Failed++
			continue
		}
		after, merr := json.Marshal(map[string]any{"imap_name": config.Inbox, "labels": []string{mailops.InboxLabel}})
		if merr != nil {
			return out, merr
		}
		if err := a.settleSnooze(ctx, ticket, ledger.Result{State: ledger.Done, After: after}, s.ID, "returned", ""); err != nil {
			return out, err
		}
		out.Written++
		out.count(live.SnoozeReturn, 1)
	}
	if out.Written > 0 {
		if err := a.Resync(ctx, account); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (a *Applier) returnSnooze(ctx context.Context, client mail.Client, account string, s snoozeRow) error {
	size, err := mailops.BatchSize(ctx, client)
	if err != nil {
		return err
	}
	if config.From(ctx).Preset(account) == config.Gmail {
		return mailops.StoreLabels(ctx, client, s.IMAPName, s.UIDValidity, []uint32{s.UID}, size,
			[]string{mailops.InboxLabel}, []string{executors.SnoozedLabel})
	}
	return mailops.MoveUIDs(ctx, client, s.IMAPName, config.Inbox, s.UIDValidity, []uint32{s.UID}, size,
		func([]uint32, *mail.CopyUID) error { return nil })
}

func (a *Applier) settleSnooze(ctx context.Context, ticket ledger.Ticket, result ledger.Result, id int64, state, reason string) error {
	err := a.Live.Store.Settle(ctx, ticket, result, func(ctx context.Context, tx *sql.Tx) error {
		return finishSnoozeTx(ctx, tx, a.now(), id, state, reason)
	})
	var settled *ledger.SettledMeanwhile
	if errors.As(err, &settled) {
		return a.finishSnooze(ctx, id, state, reason)
	}
	return err
}

func (a *Applier) finishSnooze(ctx context.Context, id int64, state, reason string) error {
	_, err := a.Live.DB.Write.ExecContext(ctx, "UPDATE snoozes SET state = ?, returned_ts = ?, error = ? WHERE id = ? AND state = 'pending'",
		state, a.now(), nullableText(reason), id)
	return err
}

func finishSnoozeTx(ctx context.Context, tx *sql.Tx, now float64, id int64, state, reason string) error {
	_, err := tx.ExecContext(ctx, "UPDATE snoozes SET state = ?, returned_ts = ?, error = ? WHERE id = ? AND state = 'pending'",
		state, now, nullableText(reason), id)
	return err
}

func nullableText(reason string) any {
	if reason == "" {
		return nil
	}
	return reason
}
