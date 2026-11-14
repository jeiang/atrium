package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/safety"
)

const CorrectName application.Name = "mail.correct"

type CorrectMode string

const (
	Immediate CorrectMode = "immediate"
	Deferred  CorrectMode = "deferred"
)

type CorrectInput struct {
	MessageID int64       `json:"message_id"`
	Folder    string      `json:"folder"`
	Mode      CorrectMode `json:"mode"`
}
type CorrectResult struct {
	Correction corrections.Correction `json:"correction"`
	Note       string                 `json:"note"`
	Outcome    string                 `json:"outcome"`
	EffectIDs  []int64                `json:"effect_ids"`
}

var correctionLog = logx.Logger("atrium.web")

func (s *Service) WithApplier(a *applier.Applier) *Service { s.applier = a; return s }
func DescribeCorrect() application.Definition {
	return application.Describe[CorrectInput, CorrectResult](CorrectName, application.Command, []application.Authority{application.Operator}, application.Conditional, true)
}
func (s *Service) Correct(ctx context.Context, in CorrectInput) (application.Change[CorrectResult], error) {
	var out application.Change[CorrectResult]
	target := strconv.FormatInt(in.MessageID, 10)
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		recorded, err := corrections.RecordTx(ctx, tx, in.MessageID, in.Folder, corrections.Source, s.now())
		if err != nil {
			return err
		}
		out.Data.Correction, out.Data.Outcome, out.Data.EffectIDs = recorded, "deferred", []int64{}
		if in.Mode == Immediate {
			return nil
		}
		out.AuditID, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(CorrectName), Outcome: state.OK, Target: target, DetailsJSON: "{}"})
		return err
	})
	if err != nil {
		var rejected corrections.Error
		if errors.As(err, &rejected) {
			err = &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err}
		}
		return application.Change[CorrectResult]{}, s.auditRefusal(ctx, CorrectName, target, err)
	}
	if in.Mode == Immediate {
		var outcome state.Outcome
		var effects []int64
		var deferred bool
		out.Data.Note, outcome, effects, deferred = s.moveCorrection(ctx, out.Data.Correction.Account, in.MessageID)
		out.AuditID = s.writeAudit(ctx, CorrectName, target, outcome, effects)
		out.EffectIDs = effects
		out.Data.EffectIDs = append(out.Data.EffectIDs, effects...)
		switch {
		case outcome == state.OK && !deferred:
			out.Data.Outcome = "ok"
		case outcome == state.OK:
		case outcome == state.Refused:
			out.Data.Outcome = "refused"
		case outcome == state.Unknown:
			out.Data.Outcome = "unknown"
		default:
			out.Data.Outcome = "failed"
		}
	}
	return out, nil
}

func (s *Service) correctionEffects(ctx context.Context, id, after int64) ([]int64, bool) {
	ids, unknown, err := ledger.AutoEffectsSince(ctx, s.db.Read, id, after)
	if err != nil {
		correctionLog.Error(fmt.Sprintf("reading the effects of corrected message %d failed", id), logx.Err, err.Error())
		return nil, false
	}
	return ids, unknown
}

type appliedOutcome struct {
	ran     bool
	written int
	unmoved int
}

func (s *Service) moveCorrection(ctx context.Context, account string, id int64) (string, state.Outcome, []int64, bool) {
	ctx = context.WithoutCancel(ctx)
	mode, err := mail.Mode(ctx, s.db.Read)
	if err != nil {
		correctionLog.Error(fmt.Sprintf("reading mail mode for corrected message %d failed", id), logx.Err, err.Error())
		return "", state.Failed, nil, false
	}
	if mode != safety.Live {
		return "", state.OK, nil, true
	}
	var effects []int64
	var unknown bool
	out, err := func() (out appliedOutcome, err error) {
		locks, err := db.ResourceLocks(ctx, s.cfg.DBPath, []string{mail.PassResource}, db.ShortLockWait)
		if err != nil {
			return out, err
		}
		defer func() { _ = locks.Close() }()
		after, readErr := ledger.LastEffectID(ctx, s.db.Read)
		if readErr != nil {
			correctionLog.Error(fmt.Sprintf("reading the effect ledger before moving corrected message %d failed", id), logx.Err, readErr.Error())
		}
		if readErr == nil {
			defer func() { effects, unknown = s.correctionEffects(ctx, id, after) }()
		}
		done, err := s.applier.ApplyCorrections(db.WithHeld(ctx, locks), account, []int64{id})
		return appliedOutcome{done.Ran, done.Written, done.Failed + done.Skipped}, err
	}()
	if err != nil {
		correctionLog.Error(fmt.Sprintf("moving corrected message %d failed", id), logx.Err, err.Error())
		outcome := state.Failed
		switch {
		case unknown:
			outcome = state.Unknown
		case errors.As(err, new(*db.LockBusy)):
			outcome = state.Refused
		}
		return " It could not be moved yet: " + err.Error() + ".", outcome, effects, false
	}
	outcome := state.OK
	if unknown {
		outcome = state.Unknown
	}
	switch {
	case !out.ran:
		return " Moving is paused; it will move after you resume.", outcome, effects, true
	case out.written > 0:
		return " The message was moved.", outcome, effects, false
	case out.unmoved > 0:
		if !unknown {
			outcome = state.Failed
		}
		return " It could not be moved; see Decisions.", outcome, effects, false
	}
	return "", outcome, effects, false
}

func (s *Service) auditRefusal(ctx context.Context, name application.Name, target string, err error) error {
	outcome := state.Failed
	var app *application.Error
	if errors.As(err, &app) {
		switch app.Code {
		case application.Invalid:
			outcome = state.Invalid
		case application.NotFound:
			outcome = state.NotFound
		case application.Conflict:
			outcome = state.Conflict
		case application.Forbidden, application.Refused:
			outcome = state.Refused
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		outcome = state.Canceled
	}
	if _, auditErr := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: "{}"}); auditErr != nil {
		return errors.Join(err, auditErr)
	}
	return err
}
