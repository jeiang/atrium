package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings/audit"
)

func (s *Service) TakeParked(ctx context.Context, threadID int64, text string, files []json.RawMessage) (int64, bool, error) {
	active, err := journal.ActiveRun(ctx, s.DB.Read, threadID)
	if err != nil || active == nil || active.State != journal.Parked {
		return 0, false, err
	}
	question, err := PendingQuestion(ctx, s.DB.Read, threadID)
	if err != nil {
		return 0, false, err
	}
	if question != nil {
		return question.RunID, true, s.Answer(ctx, threadID, text)
	}
	if err := threads.AddAllowedURLs(ctx, s.DB, threadID, threads.URLsIn(text)); err != nil {
		return 0, false, err
	}
	marks, err := references.Marks(files)
	if err != nil {
		return 0, false, err
	}
	now := s.now()
	taken := false
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		run, err := journal.GetRun(ctx, tx, active.ID)
		if err != nil || run == nil || run.State != journal.Parked {
			return err
		}
		if err := threads.AddMarks(ctx, tx, threadID, now, marks); err != nil {
			return err
		}
		if err := loop.InterruptParked(ctx, tx, threadID, run.ID, now); err != nil {
			return err
		}
		content := append(append([]json.RawMessage{}, files...), actx.TextBlock(text))
		if _, err := journal.AppendMessageTx(ctx, tx, threadID, &run.ID, journal.User, content, now, nil); err != nil {
			return err
		}
		taken = true
		return nil
	})
	if err != nil || !taken {
		return 0, false, err
	}
	if err := s.closeAsks(ctx, threadID, active.ID); err != nil {
		return active.ID, true, err
	}
	var settled NotInterrupted
	if err := s.StartResume(ctx, active.ID); err != nil && !errors.As(err, &settled) {
		return active.ID, true, err
	}
	return active.ID, true, nil
}

func (s *Service) closeAsks(ctx context.Context, threadID, runID int64) error {
	pending, err := proposals.PendingInThread(ctx, s.DB.Read, threadID)
	if err != nil {
		return err
	}
	who := proposals.Resolver{Actor: audit.Actor(ctx), Source: string(audit.SourceOf(ctx))}
	for _, p := range pending {
		if p.Kind != permission.Kind {
			continue
		}
		full, err := proposals.Get(ctx, s.DB.Read, p.ID)
		if err != nil {
			return err
		}
		if full == nil {
			continue
		}
		var payload permission.Payload
		if err := json.Unmarshal(full.Payload, &payload); err != nil {
			return err
		}
		if payload.RunID != runID {
			continue
		}
		var gone proposals.ProposalError
		if err := proposals.Dismiss(ctx, s.DB, p.ID, proposals.Dismissed, who, s.now()); err != nil && !errors.As(err, &gone) {
			return err
		}
		if err := notify.Withdraw(ctx, s.DB.Write, fmt.Sprintf("proposal:%d", p.ID), s.now()); err != nil {
			return err
		}
	}
	return nil
}
