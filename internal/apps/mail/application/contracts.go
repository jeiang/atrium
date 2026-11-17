package application

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/backlog"
	"github.com/jeiang/atrium/internal/apps/mail/corrections"
	"github.com/jeiang/atrium/internal/apps/mail/drafts"
	"github.com/jeiang/atrium/internal/apps/mail/executors"
	"github.com/jeiang/atrium/internal/apps/mail/live"
	"github.com/jeiang/atrium/internal/apps/mail/mailops"
	"github.com/jeiang/atrium/internal/apps/mail/message"
	"github.com/jeiang/atrium/internal/apps/mail/replay"
	"github.com/jeiang/atrium/internal/apps/mail/router"
	"github.com/jeiang/atrium/internal/apps/mail/ruleedit"
	"github.com/jeiang/atrium/internal/apps/mail/rules"
	"github.com/jeiang/atrium/internal/apps/mail/run"
	"github.com/jeiang/atrium/internal/apps/mail/scans"
	"github.com/jeiang/atrium/internal/apps/mail/scanutil"
	"github.com/jeiang/atrium/internal/apps/mail/search"
	"github.com/jeiang/atrium/internal/apps/mail/tracking"
	"github.com/jeiang/atrium/internal/apps/mail/triage"
	"github.com/jeiang/atrium/internal/apps/mail/watches"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/pause"
	"github.com/jeiang/atrium/internal/review"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/unit"
)

type (
	Skip                   = mailops.Skip
	TextsFunc              = embed.TextsFunc
	ServiceUnavailable     = httpx.ServiceUnavailable
	SearchFilters          = search.Filters
	NotConfigured          = config.NotConfigured
	TrackingQuotaError     = tracking.QuotaError
	TrackingError          = tracking.Error
	TrackingStatusError    = tracking.StatusError
	TrackingRateLimitError = tracking.RateLimitError
	TrackingTransportError = tracking.TransportError
	BacklogError           = backlog.Error
	ReplaySummary          = replay.Summary
	RuleError              = rules.RuleError
	RuleAction             = rules.Action
	RulePreviewResult      = ruleedit.Preview
	ScanResult             = scanutil.Result
	CorrectionError        = corrections.Error
	MessageView            = message.View
	TriageRow              = triage.Row
	TriageFolder           = triage.Folder
	TriageFilterError      = triage.FilterError
	DraftRow               = drafts.Row
	DraftStatus            = drafts.Status
	DraftError             = drafts.Error
	DraftStale             = drafts.Stale
	DraftSendOutcome       = drafts.Result
	DraftOutcomeUnknown    = drafts.OutcomeUnknown
	Package                = watches.Package
	UnknownAccount         = run.UnknownAccount
	OpenFunc               = mail.OpenFunc
	Pass                   = run.Pass
	Config                 = config.Config
)

const (
	AgentMailMaxResults    = assistant.TuningAgentMailMaxResults
	AgentMailBodyChars     = assistant.TuningAgentMailBodyChars
	AgentMailMaxLoopsLimit = assistant.TuningAgentMailMaxLoopsLimit
	PassResource           = mail.PassResource
	MailTriage             = executors.MailTriage
	TriageArchive          = executors.TriageArchive
	TriageLabel            = executors.TriageLabel
	UndoUntrack            = tracking.UndoUntrack
	ReplayStageNone        = replay.StageNone
	RuleAgeField           = rules.AgeField
	RuleAgeOp              = rules.AgeOp
	RuleActionFile         = rules.ActionFile
	RuleActionDelete       = rules.ActionDelete
	RulePreviewID          = ruleedit.PreviewID
	DraftOpen              = drafts.Draft
	DraftUnknown           = drafts.Unknown
	DraftSent              = drafts.Sent
	DraftDiscarded         = drafts.Discarded
)

var (
	LiveKinds        = live.Kinds
	RouterStages     = router.Stages
	RuleFieldNames   = rules.FieldNames
	RuleOps          = rules.Ops
	RuleValuelessOps = rules.ValuelessOps
	ScanKinds        = scans.All
)

func WallClock() float64 { return mail.WallClock() }

func ErrorMessage(err error) string { return httpx.Message(err) }

func SearchText(value string) *string { return search.Text(value) }

func SearchDay(value string, endOfDay bool) (*float64, error) { return search.Day(value, endOfDay) }

func Zone(ctx context.Context, env config.Env) (*time.Location, error) {
	return scheduler.Zone(config.From(ctx).TZName(env))
}

func SelectAccounts(ctx context.Context) ([]string, error) { return run.SelectAccounts(ctx, "") }

func (s *Service) TriageRow(ctx context.Context, messageID int64) (*triage.Row, error) {
	row, err := triage.Get(ctx, s.db.Read, messageID)
	if err != nil || row == nil {
		return row, err
	}
	rows := []triage.Row{*row}
	if err := linkRules(ctx, s.db.Read, rows); err != nil {
		return nil, err
	}
	return &rows[0], nil
}

func (s *Service) Paused() bool { return pause.Paused(s.cfg.PausePath()) }

func (s *Service) ScansEnabled(ctx context.Context) bool {
	flag, err := settings.From(ctx).Resolve(ctx, s.db.Read, module.FlagKey(unit.Mail))
	if err != nil || flag.Value != "true" {
		return false
	}
	reason, err := endpoint.EmbeddingConfigured(ctx, s.db.Read)
	return err == nil && reason == ""
}

func AccountNames(ctx context.Context) []string {
	var names []string
	for _, a := range config.From(ctx).Accounts() {
		names = append(names, a.Name)
	}
	return names
}

func Exclusive(ctx context.Context, dbPath string) (*db.Lock, error) {
	return db.Exclusive(ctx, dbPath, db.ShortLockWait)
}

func PassLocks(ctx context.Context, dbPath string) (context.Context, *db.LockSet, error) {
	locks, err := db.ResourceLocks(ctx, dbPath, []string{mail.PassResource}, db.ShortLockWait)
	if err != nil {
		return nil, nil, err
	}
	return db.WithHeld(ctx, locks), locks, nil
}

func (s *Service) ManualScans(ctx context.Context, at float64) (map[string]any, error) {
	return scans.Manual(ctx, s.db, s.cfg, s.Scans(at))
}

func ReviewDraft(row DraftRow, unexpected []string) review.Draft {
	return drafts.ReviewDraft(row, unexpected)
}
