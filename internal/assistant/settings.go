package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/settings"
)

const module = "assistant"

const (
	KeyBudgetUnattended = "budget_unattended_share"
	KeyBudgetChat       = "budget_chat_share"

	KeyDefaultModel        = "default_model"
	KeyDefaultEffort       = "default_effort"
	KeyDefaultContextLimit = "default_context_limit"
	KeyEffortCron          = "agent_effort_cron"
	KeyEffortNightly       = "agent_effort_nightly"
	KeyEffortSubagent      = "agent_effort_subagent"
	KeyEffortSub           = "agent_effort_sub"

	KeyWebSearchMaxUses = "agent_web_search_max_uses"

	KeyStubTokens      = "agent_stub_tokens"
	KeyPreviewChars    = "agent_preview_chars"
	KeyManifestResults = "agent_manifest_results"

	KeyJournalLeaseSeconds = "agent_journal_lease_seconds"

	KeySubagentMaxCostUSD    = "agent_subagent_max_cost_usd"
	KeySubagentMaxSteps      = "agent_subagent_max_steps"
	KeySubagentMaxConcurrent = "agent_subagent_max_concurrent"
	KeySubagentDigestChars   = "agent_subagent_digest_chars"
	KeySubagentDescChars     = "agent_subagent_description_chars"
	KeySubagentPromptChars   = "agent_subagent_prompt_chars"
	KeySubagentTaskChars     = "agent_subagent_task_chars"
	KeySubagentContextIDs    = "agent_subagent_context_ids"

	KeyDreamingMaxCost = "dreaming_max_cost_usd"
	KeyDreamingMonthly = "dreaming_monthly_usd"

	KeyNightlyMaxThreads  = "agent_nightly_max_threads"
	KeyNightlyThreadChars = "agent_nightly_thread_chars"
	KeyNightlyTotalChars  = "agent_nightly_total_chars"

	KeyFileCapBytes    = "agent_file_cap_bytes"
	KeyMessageCapBytes = "agent_message_cap_bytes"
	KeyFileNameChars   = "agent_file_name_chars"

	KeyRefTokens               = "agent_ref_tokens"
	KeyProjectNameChars        = "agent_project_name_chars"
	KeyProjectInstructionChars = "agent_project_instruction_chars"
	KeyPlanItems               = "agent_workspace_plan_items"
	KeyPlanItemChars           = "agent_workspace_plan_item_chars"

	KeyWebfetchMaxBytes     = "agent_webfetch_max_bytes"
	KeyWebfetchTimeout      = "agent_webfetch_timeout_seconds"
	KeyWebfetchMaxRedirects = "agent_webfetch_max_redirects"
	KeyWebfetchPageChars    = "agent_webfetch_page_text_chars"
	KeyWebfetchTextDefault  = "agent_webfetch_text_default"
	KeyWebfetchUserAgent    = "agent_webfetch_user_agent"

	KeyContextMaxIDs       = "agent_context_max_ids"
	KeyContextSummaryChars = "agent_context_summary_chars"
	KeyContextMaxSources   = "agent_context_max_sources"
	KeyRecallPageChars     = "agent_recall_page_chars"
	KeyRecallMaxPageChars  = "agent_recall_max_page_chars"
	KeyKBWriteChars        = "agent_kb_write_chars"
	KeyKBProfileTokens     = "agent_kb_profile_tokens"
	KeyKBRetrievalTokens   = "agent_kb_retrieval_tokens"
	KeyKBRetrievalDistance = "agent_kb_retrieval_distance"

	KeyMailMaxResults    = "agent_mail_max_results"
	KeyMailSnippetChars  = "agent_mail_snippet_chars"
	KeyMailBodyChars     = "agent_mail_body_chars"
	KeyMailMaxLoopsLimit = "agent_mail_max_loops_limit"

	KeyNotionMaxResults  = "agent_notion_max_results"
	KeyNotionPageChars   = "agent_notion_page_chars"
	KeyNotionStaleSecs   = "agent_notion_stale_seconds"
	KeyNotionSnippet     = "agent_notion_snippet_chars"
	KeyNotionMaxMarkdown = "agent_notion_max_markdown_chars"

	KeyMediaTorrentLimit = "agent_media_torrent_limit"

	KeyGrafanaTimeout    = "agent_grafana_timeout_seconds"
	KeyGrafanaSeries     = "agent_grafana_max_series"
	KeyGrafanaMetricRows = "agent_grafana_max_metric_rows"
	KeyGrafanaLogRows    = "agent_grafana_max_log_rows"
	KeyGrafanaAlerts     = "agent_grafana_max_alerts"
	KeyGrafanaDashboards = "agent_grafana_max_dashboards"
	KeyGrafanaCellChars  = "agent_grafana_cell_chars"
)

const DefaultContextLimit = 90_000

var ContextLimits = []int64{90_000, 200_000, 500_000, 1_000_000}

func contextLimitChoices() []string {
	out := make([]string, len(ContextLimits))
	for i, n := range ContextLimits {
		out[i] = strconv.FormatInt(n, 10)
	}
	return out
}

func init() {
	def := func(key string, kind settings.Type, surface settings.Surface, value string, check func(string) error) settings.Def {
		return settings.Value(surface, settings.Def{Key: key, Module: module, Type: kind, Default: value, Check: check})
	}
	adv := func(key string, kind settings.Type, value string, check func(string) error) settings.Def {
		return def(key, kind, settings.Advanced, value, check)
	}
	capped := func(key string, kind settings.Type, value string, check func(string) error) settings.Def {
		return def(key, kind, settings.Advanced, value, check)
	}
	count := func(key string, value, lo, hi int64) settings.Def {
		d := capped(key, settings.Int, strconv.FormatInt(value, 10), settings.Between(float64(lo), float64(hi)))
		d.Min, d.Max = lo, hi
		return d
	}
	effort := func(key, value string) settings.Def {
		d := adv(key, settings.Enum, value, nil)
		d.Choices = pricing.Efforts
		return d
	}
	chatModel := def(KeyDefaultModel, settings.Enum, settings.UI, pricing.DefaultModel, nil)
	chatModel.Choices = pricing.IDs()
	chatEffort := def(KeyDefaultEffort, settings.Enum, settings.UI, pricing.XHigh, nil)
	chatEffort.Choices = pricing.Efforts
	contextLimit := def(KeyDefaultContextLimit, settings.Enum, settings.UI, strconv.FormatInt(DefaultContextLimit, 10), nil)
	contextLimit.Choices = contextLimitChoices()
	chat := capped(KeyBudgetChat, settings.Float, "1", settings.Within(0, 1))
	chat.Peers = Ordered([]string{KeyBudgetUnattended}, nil)
	chat = settings.Shown("Chat stops at this share of the budget", chat)
	fileCap := count(KeyFileCapBytes, 32<<20, 1<<20, 128<<20)
	messageCap := count(KeyMessageCapBytes, 64<<20, 1<<20, 256<<20)
	messageCap.Peers = Ordered([]string{KeyFileCapBytes}, nil)
	fileCap.Peers = Ordered(nil, []string{KeyMessageCapBytes})
	recallPage := count(KeyRecallPageChars, 8000, 100, 100000)
	recallPage.Peers = Ordered(nil, []string{KeyRecallMaxPageChars})
	recallMax := count(KeyRecallMaxPageChars, 16000, 100, 100000)
	recallMax.Peers = Ordered([]string{KeyRecallPageChars}, nil)
	settings.MustRegister(
		chat,
		chatModel,
		chatEffort,
		contextLimit,
		settings.Shown("Scheduled run effort", effort(KeyEffortCron, "high")),
		settings.Shown("Nightly run effort", effort(KeyEffortNightly, "high")),
		settings.Shown("Research effort", effort(KeyEffortSubagent, "medium")),
		effort(KeyEffortSub, "medium"),
		count(KeyWebSearchMaxUses, 5, 1, 20),

		adv(KeyStubTokens, settings.Int, "2000", settings.Between(100, 50000)),
		adv(KeyPreviewChars, settings.Int, "600", settings.Between(50, 10000)),
		count(KeyManifestResults, 30, 1, 500),

		adv(KeyJournalLeaseSeconds, settings.Int, "1800", settings.Between(1800, 86400)),

		settings.Shown("Research run cost cap (USD)", capped(KeySubagentMaxCostUSD, settings.Float, "0.25", settings.Within(0, 2))),
		count(KeySubagentMaxSteps, 12, 1, 24),
		count(KeySubagentMaxConcurrent, 2, 1, 16),
		count(KeySubagentDigestChars, 6000, 100, 100000),
		count(KeySubagentDescChars, 160, 20, 2000),
		count(KeySubagentPromptChars, 4000, 100, 100000),
		count(KeySubagentTaskChars, 4000, 100, 100000),
		count(KeySubagentContextIDs, 20, 1, 200),

		settings.Shown("Nightly run cost cap (USD)", capped(KeyDreamingMaxCost, settings.Float, "0.30", settings.Within(0, 10))),
		settings.Shown("Nightly monthly cap (USD)", capped(KeyDreamingMonthly, settings.Float, "3", settings.Within(0, 100))),
		count(KeyNightlyMaxThreads, 30, 1, 500),
		count(KeyNightlyThreadChars, 3000, 100, 100000),
		count(KeyNightlyTotalChars, 40000, 1000, 400000),

		fileCap,
		messageCap,
		count(KeyFileNameChars, 80, 8, 200),
		count(KeyRefTokens, 8000, 1000, 32000),

		count(KeyProjectNameChars, 80, 8, 500),
		count(KeyProjectInstructionChars, 20000, 100, 200000),
		count(KeyPlanItems, 20, 1, 100),
		count(KeyPlanItemChars, 200, 20, 2000),

		count(KeyWebfetchMaxBytes, 2<<20, 1024, 16<<20),
		count(KeyWebfetchTimeout, 10, 1, 120),
		count(KeyWebfetchMaxRedirects, 5, 0, 10),
		count(KeyWebfetchPageChars, 60000, 1000, 500000),
		count(KeyWebfetchTextDefault, 12000, 100, 200000),
		adv(KeyWebfetchUserAgent, settings.String, "atrium-webfetch/1", headerValue),

		count(KeyContextMaxIDs, 50, 1, 500),
		count(KeyContextSummaryChars, 4000, 100, 100000),
		count(KeyContextMaxSources, 100, 1, 1000),
		recallPage,
		recallMax,
		count(KeyKBWriteChars, 4000, 200, 20000),
		count(KeyKBProfileTokens, 2000, 200, 20000),
		count(KeyKBRetrievalTokens, 1500, 0, 20000),
		capped(KeyKBRetrievalDistance, settings.Float, "0.35", settings.Within(0, 2)),

		count(KeyMailMaxResults, 20, 1, 200),
		count(KeyMailSnippetChars, 300, 20, 5000),
		count(KeyMailBodyChars, 8000, 100, 100000),
		count(KeyMailMaxLoopsLimit, 100, 1, 1000),

		count(KeyNotionMaxResults, 10, 1, 100),
		count(KeyNotionPageChars, 12000, 100, 200000),
		adv(KeyNotionStaleSecs, settings.Int, "86400", settings.Between(60, 2592000)),
		count(KeyNotionSnippet, 300, 20, 5000),
		count(KeyNotionMaxMarkdown, 100000, 1000, 1000000),

		count(KeyMediaTorrentLimit, 50, 1, 500),

		count(KeyGrafanaTimeout, 15, 1, 120),
		count(KeyGrafanaSeries, 20, 1, 200),
		count(KeyGrafanaMetricRows, 50, 1, 1000),
		count(KeyGrafanaLogRows, 200, 1, 2000),
		count(KeyGrafanaAlerts, 50, 1, 500),
		count(KeyGrafanaDashboards, 20, 1, 200),
		count(KeyGrafanaCellChars, 300, 20, 5000),
	)
}

func headerValue(value string) error {
	if strings.TrimSpace(value) == "" {
		return settings.Invalid("must not be blank")
	}
	for _, c := range value {
		if c < 0x20 || c > 0x7e {
			return settings.Invalid("must be printable ASCII")
		}
	}
	return nil
}

func Ordered(below, above []string) func(string, func(string) (string, error)) error {
	return func(value string, peer func(string) (string, error)) error {
		own, err := numx.ParseFloat(value)
		if err != nil {
			return settings.Invalid("must be a number")
		}
		for _, key := range below {
			text, err := peer(key)
			if err != nil {
				return err
			}
			if other, err := numx.ParseFloat(text); err == nil && own < other {
				return settings.Invalid(fmt.Sprintf("must be at least %s (%s)", key, text))
			}
		}
		for _, key := range above {
			text, err := peer(key)
			if err != nil {
				return err
			}
			if other, err := numx.ParseFloat(text); err == nil && own > other {
				return settings.Invalid(fmt.Sprintf("must be at most %s (%s)", key, text))
			}
		}
		return nil
	}
}

type Reader struct {
	ctx context.Context
	q   db.Querier
	err error
}

func Read(ctx context.Context, q db.Querier) *Reader { return &Reader{ctx: ctx, q: q} }

func (r *Reader) Err() error { return r.err }

func (r *Reader) resolve(key string) settings.Resolved {
	if r.err != nil {
		return settings.Resolved{}
	}
	found, err := settings.From(r.ctx).Resolve(r.ctx, r.q, key)
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
