package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/deletions"
	"github.com/jeiang/atrium/internal/apps/mail/live"
	"github.com/jeiang/atrium/internal/apps/mail/replay"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	StatusName        application.Name = "mail.status"
	StatusConfirmName application.Name = "mail.status-confirm"
	GoLiveName        application.Name = "mail.go-live"
	StatusRebaseName  application.Name = "mail.status-rebaseline"
	ReplayRunName     application.Name = "mail.replay-run"

	ConfirmGoLive     = "go_live"
	ConfirmRebaseline = "rebaseline"

	WatchDays    = 7
	confirmTTL   = 10 * 60
	replayKey    = "replay_last"
	confirmKeyOf = "confirm_"
)

var ConfirmActions = []string{ConfirmGoLive, ConfirmRebaseline}

type MailStatus struct {
	Mode         string          `json:"mode"`
	Paused       bool            `json:"paused"`
	Eligible     bool            `json:"eligible"`
	Unmet        []string        `json:"unmet"`
	Accounts     []string        `json:"accounts"`
	Baseline     StatusBaseline  `json:"baseline"`
	Shadow       StatusShadow    `json:"shadow"`
	Replay       *ReplayRun      `json:"replay" api:"nullable"`
	Deletions    StatusDeletions `json:"deletions"`
	WatchClosely StatusWatch     `json:"watch_closely"`
}

type StatusBaseline struct {
	Messages int64    `json:"messages"`
	TakenAt  *float64 `json:"taken_at" api:"nullable"`
}

type StatusShadow struct {
	Actions map[string]map[string]int64 `json:"actions"`
}

type StatusDeletions struct {
	PendingProposals int `json:"pending_proposals"`
}

type StatusWatch struct {
	Active bool     `json:"active"`
	Mode   *string  `json:"mode" api:"nullable"`
	Since  *float64 `json:"since" api:"nullable"`
	Until  *float64 `json:"until" api:"nullable"`
}

type ReplayRun struct {
	RunID    int64   `json:"run_id"`
	At       float64 `json:"at"`
	Messages int     `json:"messages"`
	Differ   int     `json:"differ"`
}

type ConfirmInput struct {
	Action string `json:"action"`
}

type ConfirmEffect struct {
	Action    string   `json:"action"`
	Token     string   `json:"confirm_token"`
	Expires   float64  `json:"expires_at"`
	Accounts  []string `json:"accounts"`
	WriteCap  int      `json:"write_cap"`
	Baseline  int64    `json:"baseline"`
	Statement string   `json:"statement"`
	Pause     string   `json:"pause"`
}

type ConfirmedInput struct {
	Token string `json:"confirm_token"`
}

type GoLiveResult struct {
	Mode     string   `json:"mode"`
	Accounts []string `json:"accounts"`
}

func DescribeStatus() application.Definition {
	return application.Describe[struct{}, MailStatus](StatusName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeStatusConfirm() application.Definition {
	return application.Describe[ConfirmInput, ConfirmEffect](StatusConfirmName, application.Command, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeGoLive() application.Definition {
	return application.Describe[ConfirmedInput, GoLiveResult](GoLiveName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeStatusRebaseline() application.Definition {
	return application.Describe[ConfirmedInput, RebaselineResult](StatusRebaseName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeReplayRun() application.Definition {
	return application.Describe[struct{}, ReplayRun](ReplayRunName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func OperatorCaller(ctx context.Context) error {
	if !operatorSource(ctx) {
		return &application.Error{Code: application.Forbidden, Reason: "operator_only", Message: "Only the operator can do this."}
	}
	if p, ok := authapp.PrincipalOf(ctx); ok && p.Agent {
		return &application.Error{Code: application.Forbidden, Reason: "agent_caller", Message: "Agent-marked callers cannot change the mail mode or baseline."}
	}
	return nil
}

func (s *Service) Status(ctx context.Context, _ struct{}) (application.Read[MailStatus], error) {
	var out MailStatus
	rev, err := s.snapshot(ctx, StatusName, nil, func(q db.Querier) error {
		var err error
		out, err = s.status(ctx, q)
		return err
	})
	return application.Read[MailStatus]{Revision: rev, Data: out}, err
}

func (s *Service) status(ctx context.Context, q db.Querier) (MailStatus, error) {
	ls, err := s.liveService().Status(ctx)
	if err != nil {
		return MailStatus{}, err
	}
	out := MailStatus{Mode: ls.Mode, Paused: ls.Paused, Baseline: StatusBaseline{Messages: ls.Baseline}, Shadow: StatusShadow{Actions: ls.Actions}}
	if ls.GoliveTS != nil {
		if ts, ok := parseTS(*ls.GoliveTS); ok {
			out.Baseline.TakenAt = &ts
		}
	}
	if out.Accounts, out.Unmet, err = s.eligibility(ctx, q, ls.Mode); err != nil {
		return MailStatus{}, err
	}
	out.Eligible = len(out.Unmet) == 0
	if out.Replay, err = lastReplay(ctx, q); err != nil {
		return MailStatus{}, err
	}
	if out.Deletions.PendingProposals, err = proposals.CountPendingKind(ctx, q, deletions.Kind); err != nil {
		return MailStatus{}, err
	}
	out.WatchClosely, err = watch(ctx, q, s.now())
	return out, err
}

func (s *Service) eligibility(ctx context.Context, q db.Querier, mode string) ([]string, []string, error) {
	accounts, err := SelectAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	unmet := []string{}
	if mode == config.Live {
		unmet = append(unmet, "already_live")
	}
	if len(accounts) == 0 {
		unmet = append(unmet, "no_accounts")
	}
	held, err := ledger.CountUnresolved(ctx, q, ledger.AutoOrigin, "operator_send", OperatorOrigin)
	if err != nil {
		return nil, nil, err
	}
	if held > 0 {
		unmet = append(unmet, "unresolved_writes")
	}
	return append([]string{}, accounts...), unmet, nil
}

func watch(ctx context.Context, q db.Querier, now float64) (StatusWatch, error) {
	ts, mode, ok, err := audit.LastChange(ctx, q, mail.KeyMode)
	if err != nil || !ok {
		return StatusWatch{}, err
	}
	until := ts + WatchDays*86400
	w := StatusWatch{Active: now < until, Since: &ts, Until: &until}
	if mode.Valid {
		w.Mode = &mode.String
	}
	return w, nil
}

func lastReplay(ctx context.Context, q db.Querier) (*ReplayRun, error) {
	raw, err := live.GetState(ctx, q, replayKey)
	if err != nil || raw == nil || *raw == "" {
		return nil, err
	}
	var r ReplayRun
	if err := json.Unmarshal([]byte(*raw), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Service) RecordReplay(ctx context.Context, runID int64, summary replay.Summary) (ReplayRun, error) {
	r := ReplayRun{RunID: runID, At: s.now(), Messages: summary.Messages, Differ: summary.Differ}
	raw, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	return r, s.db.WithTx(ctx, func(tx *sql.Tx) error { return live.SetState(ctx, tx, replayKey, string(raw)) })
}

func (s *Service) ReplayRunAudit(ctx context.Context, r ReplayRun, err error) int64 {
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	return s.writeAudit(ctx, ReplayRunName, fmt.Sprint(r.RunID), outcome, nil)
}

type confirmRecord struct {
	Hash    string  `json:"hash"`
	Expires float64 `json:"expires"`
	Digest  string  `json:"digest"`
}

func (s *Service) Confirm(ctx context.Context, in ConfirmInput) (application.Change[ConfirmEffect], error) {
	if err := OperatorCaller(ctx); err != nil {
		return application.Change[ConfirmEffect]{}, err
	}
	effect, digest, err := s.effectOf(ctx, in.Action)
	if err != nil {
		return application.Change[ConfirmEffect]{}, err
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return application.Change[ConfirmEffect]{}, err
	}
	effect.Token = hex.EncodeToString(buf)
	effect.Expires = s.now() + confirmTTL
	raw, err := json.Marshal(confirmRecord{Hash: hashToken(effect.Token), Expires: effect.Expires, Digest: digest})
	if err != nil {
		return application.Change[ConfirmEffect]{}, err
	}
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error { return live.SetState(ctx, tx, confirmKeyOf+in.Action, string(raw)) })
	return application.Change[ConfirmEffect]{Data: effect}, err
}

func (s *Service) effectOf(ctx context.Context, action string) (ConfirmEffect, string, error) {
	mode, err := s.Mode(ctx)
	if err != nil {
		return ConfirmEffect{}, "", err
	}
	accounts, unmet, err := s.eligibility(ctx, s.db.Read, mode)
	if err != nil {
		return ConfirmEffect{}, "", err
	}
	limit, err := writeCap(ctx, s.db.Read)
	if err != nil {
		return ConfirmEffect{}, "", err
	}
	var baseline int64
	if err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE "+live.BaselineWhere).Scan(&baseline); err != nil {
		return ConfirmEffect{}, "", err
	}
	e := ConfirmEffect{Action: action, Accounts: accounts, WriteCap: limit, Baseline: baseline,
		Pause: "Pause (Assistant › Authority) stops every automatic mail write until resume; it does not change the mode."}
	list := strings.Join(accounts, ", ")
	switch action {
	case ConfirmGoLive:
		if len(unmet) > 0 {
			return ConfirmEffect{}, "", notEligible(unmet)
		}
		e.Statement = fmt.Sprintf("Sets mail_mode to live. Automatic filing, flagging and deletion start writing to the mailboxes of %s, at most %d writes per account per pass. The next pass takes the go-live baseline if none exists: about %d read, unflagged inbox messages are never touched by the automatic inbox pass.", list, limit, baseline)
	case ConfirmRebaseline:
		e.Statement = fmt.Sprintf("Syncs %s and retakes the go-live baseline: about %d read, unflagged inbox messages become the set the automatic inbox pass never touches, and the go-live time becomes now, so older mail is no longer filed automatically. Mail mode stays %s.", list, baseline, mode)
	default:
		return ConfirmEffect{}, "", &application.Error{Code: application.Invalid, Message: "action must be go_live or rebaseline",
			Fields: []application.Field{{Name: "action", Code: "invalid", Message: "Use go_live or rebaseline."}}}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{action, mode, list, fmt.Sprint(limit)}, "\x00")))
	return e, hex.EncodeToString(sum[:]), nil
}

func notEligible(unmet []string) error {
	return &application.Error{Code: application.Refused, Reason: "not_eligible", Message: "Go-live is not possible now: " + strings.Join(unmet, ", ") + "."}
}

func (s *Service) Redeem(ctx context.Context, action, token string) error {
	if err := OperatorCaller(ctx); err != nil {
		return err
	}
	missing := &application.Error{Code: application.Refused, Reason: "confirm_required", Message: "This needs the confirm token from POST /mail/status/confirm."}
	if token == "" {
		return missing
	}
	_, digest, err := s.effectOf(ctx, action)
	if err != nil {
		return err
	}
	var bad error
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		raw, err := live.GetState(ctx, tx, confirmKeyOf+action)
		if err != nil {
			return err
		}
		var rec confirmRecord
		if raw == nil || *raw == "" || json.Unmarshal([]byte(*raw), &rec) != nil || subtle.ConstantTimeCompare([]byte(rec.Hash), []byte(hashToken(token))) != 1 {
			bad = missing
			return nil
		}
		if rec.Expires < s.now() || rec.Digest != digest {
			bad = &application.Error{Code: application.Refused, Reason: "confirm_stale", Message: "The confirmation expired or the state changed since; confirm again."}
		}
		return live.SetState(ctx, tx, confirmKeyOf+action, "")
	})
	if err != nil {
		return err
	}
	return bad
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) GoLive(ctx context.Context, in ConfirmedInput, set func(context.Context) error) (application.Change[GoLiveResult], error) {
	err := s.Redeem(ctx, ConfirmGoLive, in.Token)
	var out GoLiveResult
	if err == nil {
		err = set(audit.With(ctx, audit.Actor(ctx), audit.SourceOf(ctx)))
	}
	if err == nil {
		out.Mode = config.Live
		out.Accounts, err = SelectAccounts(ctx)
	}
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	id := s.writeAudit(ctx, GoLiveName, "", outcome, nil)
	return application.Change[GoLiveResult]{Data: out, AuditID: id}, err
}

func (s *Service) ConfirmedRebaseline(ctx context.Context, redeem func(context.Context) error) (application.Change[RebaselineResult], error) {
	if err := redeem(ctx); err != nil {
		id := s.writeAudit(ctx, StatusRebaseName, "", auditOutcome(err), nil)
		return application.Change[RebaselineResult]{AuditID: id}, err
	}
	out, err := s.rebaseline(ctx)
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	id := s.writeAudit(ctx, StatusRebaseName, "", outcome, nil)
	return application.Change[RebaselineResult]{Data: out, AuditID: id}, err
}

func parseTS(v string) (float64, bool) {
	var ts float64
	_, err := fmt.Sscan(strings.TrimSpace(v), &ts)
	return ts, err == nil
}
