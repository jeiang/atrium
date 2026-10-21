package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const module = "mail"

const (
	KeyMode                   = "mail_mode"
	KeyChatURL                = "mail_chat_url"
	KeyEmbedURL               = "mail_embed_url"
	KeyChatModel              = "mail_chat_model"
	KeyChatMaxTokens          = "mail_chat_max_tokens"
	KeyEmbedModel             = "mail_embed_model"
	KeyWriteCap               = "write_cap"
	KeyWriteAttempts          = "write_attempts"
	KeyDeletionInterval       = "deletion_interval_seconds"
	KeyUncertainDest          = "uncertain_dest"
	KeyBodyCapBytes           = "mail_body_cap_bytes"
	KeyFetchBatch             = "mail_fetch_batch"
	KeyEmbedBatch             = "mail_embed_batch"
	KeyFlagBatch              = "mail_flag_batch"
	KeyMailopsBatch           = "mailops_batch"
	KeySentBackfillDays       = "sent_backfill_days"
	KeyKNNK                   = "knn_k"
	KeyKNNMinShare            = "knn_min_share"
	KeyKNNMax                 = "knn_max"
	KeyRuleMinN               = "rule_min_n"
	KeyRuleMinPurity          = "rule_min_purity"
	KeyFolderMinMessages      = "folder_min_messages"
	KeyLLMNeighbors           = "llm_neighbors"
	KeyLLMTopSenders          = "llm_top_senders"
	KeyLLMBodyChars           = "llm_body_chars"
	KeyEmbedBodyChars         = "mail_embed_body_chars"
	KeyFTSBodyChars           = "mail_fts_body_chars"
	KeyIdleSettleSeconds      = "mail_idle_settle_seconds"
	KeyBackoffStartSeconds    = "mail_backoff_start_seconds"
	KeyBackoffMaxSeconds      = "mail_backoff_max_seconds"
	KeyRebalanceMinFolder     = "rebalance_min_folder"
	KeyRebalanceMinGroup      = "rebalance_min_group"
	KeyRebalanceMinShare      = "rebalance_min_share"
	KeyRebalanceMergeMinGroup = "rebalance_merge_min_group"
	KeyRebalanceMergeMinOther = "rebalance_merge_min_others"
	KeyRebalanceMergeMinShare = "rebalance_merge_min_share"
	KeyRebalanceRunsRequired  = "rebalance_runs_required"
	KeyRebalanceRunGapSeconds = "rebalance_run_gap_seconds"
	KeyRebalanceClusterKs     = "rebalance_cluster_ks"
	KeyRebalanceClusterIters  = "rebalance_cluster_iterations"
	KeyRebalanceClusterMargin = "rebalance_cluster_margin"
	KeyRebalanceClusterMinEmb = "rebalance_cluster_min_embedded"
	KeyRebalanceTopTerms      = "rebalance_top_terms"
	KeyRebalanceNameMaxChars  = "rebalance_name_max_chars"
	KeyDeletionsMinGroup      = "deletions_min_group"
	KeyDeletionsMinShare      = "deletions_min_deleted_share"
	KeyDeletionsOTPMinShare   = "deletions_otp_min_share"
	KeyDeletionsOTPOlderDays  = "deletions_otp_older_than_days"
	KeyGmailLabelsMaxMessages = "gmail_labels_max_messages"
	KeyGmailLabelsMinTarget   = "gmail_labels_min_target_messages"
	KeyGmailLabelsMinOverlap  = "gmail_labels_min_overlap"
	KeyTriageTopDests         = "triage_top_dests"
	KeyReportTopDests         = "report_top_dests"
	KeyReportDisagreeLines    = "report_disagreement_lines"
	KeyScanSampleSubjects     = "mail_scan_sample_subjects"
	KeyScanTopLabels          = "mail_scan_top_labels"
	KeySearchRRFK             = "mail_search_rrf_k"
	KeySearchBranchLimit      = "mail_search_branch_limit"
	KeySearchMaxTerms         = "mail_search_max_terms"
	KeySearchSnippetTokens    = "mail_search_snippet_tokens"
	KeySearchSnippetChars     = "mail_search_snippet_chars"
	KeyDraftsMaxBody          = "drafts_max_body"
	KeyDraftsMaxSubject       = "drafts_max_subject"
	KeyLoopsWindowDays        = "openloops_window_days"
	KeyLoopsOweMaxAgeDays     = "openloops_owe_max_age_days"
	KeyLoopsWaitingAfterDays  = "openloops_waiting_after_days"
	KeyLoopsWaitingMaxAgeDays = "openloops_waiting_max_age_days"
	KeyLoopsCommitWindowDays  = "openloops_commitment_window_days"
	KeyLoopsCommitScanLimit   = "openloops_commitment_scan_limit"
	KeyLoopsCommitsPerMail    = "openloops_commitments_per_message"
	KeyLoopsRemindAheadDays   = "openloops_remind_ahead_days"
	KeyLoopsRemindOverdueDays = "openloops_remind_overdue_days"
	KeyLoopsRemindHour        = "openloops_remind_hour"
	KeyLoopsBackfillLimit     = "openloops_backfill_limit"
	KeyLoopsSnoozeDefaultDays = "openloops_snooze_default_days"
	KeyWatchesScanLimit       = "watches_scan_limit"
	KeyWatchesPackageWindow   = "watches_package_window_days"
	KeyWatchesStalePackage    = "watches_stale_package_days"
	KeyWatchesBodyChars       = "watches_body_chars"
	KeyWatchesCarrierDomains  = "watches_carrier_domains"
	KeyBirthdaysDefaultDays   = "birthdays_default_days"
	KeyBirthdaysMaxDays       = "birthdays_max_days"
)

const modeDefault = config.Shadow

func init() {
	instance := []settings.Scope{settings.Instance}
	adv := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check, Class: settings.Advanced, Scopes: instance}
	}
	ui := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check, Class: settings.UI, Scopes: instance}
	}
	count := func(key string, def int) settings.Def {
		return adv(key, settings.Int, strconv.Itoa(def), atLeast(1))
	}
	share := func(key, def string) settings.Def {
		return adv(key, settings.Float, def, settings.Within(0, 1))
	}
	settings.MustRegister(
		settings.Def{Key: KeyMode, Module: module, Type: settings.String, Default: modeDefault, Check: modeCheck, Class: settings.Bootstrap},
		ui(KeyChatURL, settings.String, "", nil),
		ui(KeyEmbedURL, settings.String, "", nil),
		ui(KeyChatModel, settings.String, "qwen", nonBlank),
		adv(KeyChatMaxTokens, settings.Int, "80", atLeast(1)),
		adv(KeyEmbedModel, settings.String, "embeddinggemma", nonBlank),
		settings.Def{
			Key: KeyWriteCap, Module: module, Type: settings.Int, Default: "100",
			Check: settings.Between(1, 1000), Class: settings.Cap, Scopes: instance,
		},
		adv(KeyWriteAttempts, settings.Int, "3", settings.Between(1, 10)),
		adv(KeyDeletionInterval, settings.Int, "86400", atLeast(3600)),
		ui(KeyUncertainDest, settings.String, "Misc", nonBlank),
		adv(KeyBodyCapBytes, settings.Int, "65536", settings.Between(1024, 1048576)),
		count(KeyFetchBatch, 40),
		count(KeyEmbedBatch, 16),
		count(KeyFlagBatch, 500),
		count(KeyMailopsBatch, 100),
		count(KeySentBackfillDays, 90),
		adv(KeyKNNK, settings.Int, "7", settings.Between(1, 50)),
		adv(KeyKNNMinShare, settings.Float, "0.9", settings.Within(0.5, 1)),
		adv(KeyKNNMax, settings.Int, "4096", settings.Between(1, 4096)),
		count(KeyRuleMinN, 3),
		share(KeyRuleMinPurity, "0.9"),
		count(KeyFolderMinMessages, 10),
		count(KeyLLMNeighbors, 10),
		count(KeyLLMTopSenders, 3),
		count(KeyLLMBodyChars, 1500),
		count(KeyEmbedBodyChars, 1500),
		count(KeyFTSBodyChars, 20000),
		adv(KeyIdleSettleSeconds, settings.Int, "2", atLeast(0)),
		count(KeyBackoffStartSeconds, 5),
		count(KeyBackoffMaxSeconds, 300),
		count(KeyRebalanceMinFolder, 40),
		count(KeyRebalanceMinGroup, 20),
		share(KeyRebalanceMinShare, "0.1"),
		count(KeyRebalanceMergeMinGroup, 3),
		count(KeyRebalanceMergeMinOther, 10),
		share(KeyRebalanceMergeMinShare, "0.9"),
		count(KeyRebalanceRunsRequired, 2),
		count(KeyRebalanceRunGapSeconds, 14*86400),
		adv(KeyRebalanceClusterKs, settings.String, "2,3,4", clusterKsCheck),
		count(KeyRebalanceClusterIters, 15),
		adv(KeyRebalanceClusterMargin, settings.Float, "0.04", settings.Within(0, 1)),
		share(KeyRebalanceClusterMinEmb, "0.8"),
		count(KeyRebalanceTopTerms, 5),
		count(KeyRebalanceNameMaxChars, 40),
		count(KeyDeletionsMinGroup, 5),
		share(KeyDeletionsMinShare, "0.7"),
		share(KeyDeletionsOTPMinShare, "0.8"),
		count(KeyDeletionsOTPOlderDays, 7),
		count(KeyGmailLabelsMaxMessages, 10),
		count(KeyGmailLabelsMinTarget, 10),
		share(KeyGmailLabelsMinOverlap, "0.5"),
		count(KeyTriageTopDests, 15),
		count(KeyReportTopDests, 15),
		count(KeyReportDisagreeLines, 40),
		count(KeyScanSampleSubjects, 5),
		count(KeyScanTopLabels, 5),
		count(KeySearchRRFK, 60),
		count(KeySearchBranchLimit, 50),
		count(KeySearchMaxTerms, 12),
		count(KeySearchSnippetTokens, 24),
		count(KeySearchSnippetChars, 220),
		count(KeyDraftsMaxBody, 100_000),
		count(KeyDraftsMaxSubject, 300),
		count(KeyLoopsWindowDays, 120),
		count(KeyLoopsOweMaxAgeDays, 45),
		count(KeyLoopsWaitingAfterDays, 3),
		count(KeyLoopsWaitingMaxAgeDays, 60),
		count(KeyLoopsCommitWindowDays, 14),
		count(KeyLoopsCommitScanLimit, 20),
		count(KeyLoopsCommitsPerMail, 5),
		count(KeyLoopsRemindAheadDays, 1),
		count(KeyLoopsRemindOverdueDays, 14),
		ui(KeyLoopsRemindHour, settings.Int, "9", settings.Between(0, 23)),
		count(KeyLoopsBackfillLimit, 2000),
		count(KeyLoopsSnoozeDefaultDays, 3),
		count(KeyWatchesScanLimit, 25),
		count(KeyWatchesPackageWindow, 60),
		count(KeyWatchesStalePackage, 60),
		count(KeyWatchesBodyChars, 3000),
		ui(KeyWatchesCarrierDomains, settings.String, "ups.com,fedex.com,usps.com,dhl.com,usps.gov,dhl.de", domainsCheck),
		count(KeyBirthdaysDefaultDays, 14),
		count(KeyBirthdaysMaxDays, 366),
	)
}

func atLeast(lo int64) func(string) error {
	return func(value string) error {
		n, ok := py.ParseInt(value)
		if !ok {
			return settings.Invalid("must be a whole number")
		}
		if n < lo {
			return settings.Invalid("must be at least " + strconv.FormatInt(lo, 10))
		}
		return nil
	}
}

func nonBlank(value string) error {
	if py.Strip(value) == "" {
		return settings.Invalid("must not be blank")
	}
	return nil
}

func modeCheck(value string) error {
	for _, m := range config.Modes {
		if value == m {
			return nil
		}
	}
	return settings.Invalid(fmt.Sprintf("must be one of (%s, %s), got %s", py.Repr(config.Shadow), py.Repr(config.Live), py.Repr(value)))
}

func clusterKsCheck(value string) error {
	_, err := splitInts(value)
	return err
}

func domainsCheck(value string) error {
	if len(SplitList(value)) == 0 {
		return settings.Invalid("must list at least one domain")
	}
	for _, d := range SplitList(value) {
		for _, c := range d {
			if c != '.' && c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return settings.Invalid("domains are lowercase letters, digits, dots and hyphens, got " + py.Repr(d))
			}
		}
	}
	return nil
}

func SplitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = py.Strip(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitInts(value string) ([]int, error) {
	parts := SplitList(value)
	if len(parts) == 0 {
		return nil, settings.Invalid("must list at least one whole number")
	}
	out := make([]int, len(parts))
	for i, part := range parts {
		n, ok := py.ParseInt(part)
		if !ok || n < 1 || n > math.MaxInt32 {
			return nil, settings.Invalid("must be comma-separated whole numbers of at least 1, got " + py.Repr(part))
		}
		out[i] = int(n)
	}
	return out, nil
}

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type readOnly struct{ Querier }

func (readOnly) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("mail settings: read-only querier")
}

type Reader struct {
	ctx context.Context
	q   readOnly
	err error
}

func Read(ctx context.Context, q Querier) *Reader { return &Reader{ctx: ctx, q: readOnly{q}} }

func (r *Reader) Err() error { return r.err }

func (r *Reader) resolve(key string) settings.Resolved {
	if r.err != nil {
		return settings.Resolved{}
	}
	found, err := settings.Default.Resolve(r.ctx, r.q, key, 0)
	r.err = err
	return found
}

func (r *Reader) Text(key string) string { return r.resolve(key).Value }

func (r *Reader) Int(key string) int {
	found := r.resolve(key)
	if r.err != nil {
		return 0
	}
	n, err := found.Int()
	r.err = err
	return int(n)
}

func (r *Reader) Float(key string) float64 {
	found := r.resolve(key)
	if r.err != nil {
		return 0
	}
	f, err := found.Float()
	r.err = err
	return f
}

func (r *Reader) Ints(key string) []int {
	found := r.resolve(key)
	if r.err != nil {
		return nil
	}
	out, err := splitInts(found.Value)
	r.err = err
	return out
}

func (r *Reader) List(key string) []string { return SplitList(r.resolve(key).Value) }

func Mode(ctx context.Context, q Querier) (string, error) {
	found, err := settings.Default.Resolve(ctx, readOnly{q}, KeyMode, 0)
	if err != nil {
		return "", err
	}
	if err := modeCheck(found.Value); err != nil {
		return "", fmt.Errorf("%s %w", KeyMode, err)
	}
	return found.Value, nil
}

func ChatURL(ctx context.Context, q Querier) (string, error) {
	return urlOf(ctx, q, KeyChatURL)
}

func EmbedURL(ctx context.Context, q Querier) (string, error) {
	return urlOf(ctx, q, KeyEmbedURL)
}

func urlOf(ctx context.Context, q Querier, key string) (string, error) {
	found, err := settings.Default.Resolve(ctx, readOnly{q}, key, 0)
	return strings.TrimRight(found.Value, "/"), err
}

func UncertainDest(ctx context.Context, q Querier, account string) (string, error) {
	spec, ok := config.Account(account)
	if !ok || spec.UncertainDest == "" {
		return "", nil
	}
	found, err := settings.Default.Resolve(ctx, readOnly{q}, KeyUncertainDest, 0)
	return found.Value, err
}

func WithModels(ctx context.Context, q Querier) (context.Context, error) {
	r := Read(ctx, q)
	chat := llm.Options{Model: r.Text(KeyChatModel), MaxTokens: r.Int(KeyChatMaxTokens), BodyChars: r.Int(KeyLLMBodyChars), TopSenders: r.Int(KeyLLMTopSenders)}
	vec := embed.Options{Model: r.Text(KeyEmbedModel), BodyChars: r.Int(KeyEmbedBodyChars), Batch: r.Int(KeyEmbedBatch)}
	if r.Err() != nil {
		return ctx, r.Err()
	}
	return embed.WithOptions(llm.WithOptions(ctx, chat), vec), nil
}
