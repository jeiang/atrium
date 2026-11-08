package mail

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const module = "mail"

const (
	KeyMode                   = "mail_mode"
	KeyWriteCap               = "write_cap"
	KeyWriteAttempts          = "write_attempts"
	KeyDeletionInterval       = "deletion_interval_seconds"
	KeyUncertainDest          = "uncertain_dest"
	KeyBodyCapBytes           = "mail_body_cap_bytes"
	KeyFetchBatch             = "mail_fetch_batch"
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
	adv := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.Advanced, settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check})
	}
	ui := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.UI, settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check})
	}
	count := func(key string, def int) settings.Def {
		return adv(key, settings.Int, strconv.Itoa(def), atLeast(1))
	}
	share := func(key, def string) settings.Def {
		return adv(key, settings.Float, def, settings.Within(0, 1))
	}
	settings.MustRegister(
		settings.Deploy(settings.Def{Key: KeyMode, Module: module, Type: settings.String, Default: modeDefault, Check: modeCheck}),
		ui(endpoint.KeyChatURL, settings.String, "", nil),
		ui(endpoint.KeyEmbedURL, settings.String, "", nil),
		ui(endpoint.KeyChatModel, settings.String, "qwen", nonBlank),
		adv(endpoint.KeyChatMaxTokens, settings.Int, "80", atLeast(1)),
		adv(endpoint.KeyEmbedModel, settings.String, "embeddinggemma", nonBlank),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyWriteCap, Module: module, Type: settings.Int, Default: "100",
			Check: settings.Between(1, 1000)}),
		adv(KeyWriteAttempts, settings.Int, "3", settings.Between(1, 10)),
		adv(KeyDeletionInterval, settings.Int, "86400", atLeast(3600)),
		ui(KeyUncertainDest, settings.String, "Misc", nonBlank),
		adv(KeyBodyCapBytes, settings.Int, strconv.Itoa(config.BodyCapBytes), settings.Between(1024, 1048576)),
		count(KeyFetchBatch, 40),
		count(endpoint.KeyEmbedBatch, 16),
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
		count(endpoint.KeyLLMTopSenders, 3),
		count(endpoint.KeyLLMBodyChars, 1500),
		count(endpoint.KeyEmbedBodyChars, 1500),
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
	return settings.Invalid(fmt.Sprintf("must be one of (%s, %s), got %s", strconv.Quote(config.Shadow), strconv.Quote(config.Live), strconv.Quote(value)))
}

func clusterKsCheck(value string) error {
	_, err := endpoint.SplitInts(value)
	return err
}

func domainsCheck(value string) error {
	if len(endpoint.SplitList(value)) == 0 {
		return settings.Invalid("must list at least one domain")
	}
	for _, d := range endpoint.SplitList(value) {
		for _, c := range d {
			if c != '.' && c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return settings.Invalid("domains are lowercase letters, digits, dots and hyphens, got " + strconv.Quote(d))
			}
		}
	}
	return nil
}

func WallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Mode(ctx context.Context, q db.Querier) (string, error) {
	r := endpoint.Read(ctx, q)
	value := r.Text(KeyMode)
	if err := r.Err(); err != nil {
		return "", err
	}
	if err := modeCheck(value); err != nil {
		return "", fmt.Errorf("%s %w", KeyMode, err)
	}
	return value, nil
}

func UncertainDest(ctx context.Context, q db.Querier, account string) (string, error) {
	spec, ok := config.From(ctx).Account(account)
	if !ok || spec.UncertainDest() == "" {
		return "", nil
	}
	r := endpoint.Read(ctx, q)
	return r.Text(KeyUncertainDest), r.Err()
}
