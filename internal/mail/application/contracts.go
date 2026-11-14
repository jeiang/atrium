package application

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/backlog"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/replay"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/scans"
	"github.com/jeiang/atrium/internal/mail/scanutil"
	"github.com/jeiang/atrium/internal/mail/search"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
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
	Loop                   = openloops.Loop
	Commitment             = openloops.Commitment
	Package                = watches.Package
	UnknownAccount         = run.UnknownAccount
	OpenFunc               = mail.OpenFunc
)

const (
	AgentMailMaxResults    = assistant.TuningAgentMailMaxResults
	AgentMailBodyChars     = assistant.TuningAgentMailBodyChars
	AgentMailMaxLoopsLimit = assistant.TuningAgentMailMaxLoopsLimit
	BirthdaysMaxDays       = birthdays.MaxDays
	PassResource           = mail.PassResource
	MailTriage             = executors.MailTriage
	TriageArchive          = executors.TriageArchive
	TriageLabel            = executors.TriageLabel
	TriageSnooze           = executors.TriageSnooze
	UndoUntrack            = tracking.UndoUntrack
	LoopOweReply           = openloops.OweReply
	LoopWaiting            = openloops.Waiting
	CommitmentOpen         = openloops.Open
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
	LoopStates         = openloops.States
	CommitmentStatuses = openloops.CommitmentStatuses
	LiveKinds          = live.Kinds
	RouterStages       = router.Stages
	RuleFieldNames     = rules.FieldNames
	RuleOps            = rules.Ops
	RuleValuelessOps   = rules.ValuelessOps
	ScanKinds          = scans.All
)

func WallClock() float64 { return mail.WallClock() }

func ErrorMessage(err error) string { return httpx.Message(err) }

func SearchText(value string) *string { return search.Text(value) }

func SearchDay(value string, endOfDay bool) (*float64, error) { return search.Day(value, endOfDay) }

func BirthdayDate(year int, month time.Month, day int) time.Time {
	return birthdays.Date(year, month, day)
}

func Zone(ctx context.Context, env config.Env) (*time.Location, error) {
	return scheduler.Zone(config.From(ctx).TZName(env))
}

func SelectAccounts(ctx context.Context) ([]string, error) { return run.SelectAccounts(ctx, "") }

func (s *Service) TriageRow(ctx context.Context, messageID int64) (*triage.Row, error) {
	return triage.Get(ctx, s.db.Read, messageID)
}

func (s *Service) Paused() bool { return safety.IsPaused(s.cfg.PausePath()) }

func (s *Service) ScansEnabled(ctx context.Context) bool {
	flag, err := settings.From(ctx).Resolve(ctx, s.db.Read, module.FlagKey(module.Mail))
	if err != nil || flag.Value != "true" {
		return false
	}
	reason, err := endpoint.EmbeddingConfigured(ctx, s.db.Read)
	return err == nil && reason == ""
}
