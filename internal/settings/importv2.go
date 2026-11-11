package settings

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/settings/audit"
)

type ImportClass string

const (
	ImportHost     ImportClass = "host"
	ImportOperator ImportClass = "operator"
	ImportTuning   ImportClass = "tuning"
)

type ImportAction string

const (
	ImportCarry       ImportAction = "carry"
	ImportDropHost    ImportAction = "drop-host"
	ImportDropTuning  ImportAction = "drop-tuning"
	ImportRename      ImportAction = "rename"
	ImportDrop        ImportAction = "drop"
	ImportResetShadow ImportAction = "reset-for-cutover"
)

type ImportEntry struct {
	Module  string
	Key     string
	Class   ImportClass
	Action  ImportAction
	Target  string
	Default string
	Type    Type
	Choices []string
	Cutover bool
	Runtime bool
	Secret  bool
	Note    string
}

type ImportPin struct {
	Value  string
	Source string
}

type ImportSummary struct {
	Carried          int
	Renamed          int
	DropHost         int
	DropTuning       int
	Dropped          int
	ResetShadow      int
	DropUnknown      int
	UnmappedOperator int
}

func (s ImportSummary) Lines() []string {
	return []string{fmt.Sprintf(
		"import totals: carried=%d renamed=%d drop-host=%d drop-tuning=%d dropped=%d reset-for-cutover=%d drop-unknown=%d unmapped_operator_choices=%d",
		s.Carried, s.Renamed, s.DropHost, s.DropTuning, s.Dropped, s.ResetShadow, s.DropUnknown, s.UnmappedOperator)}
}

var importManifest = []ImportEntry{
	{Module: "action_links", Key: "action_link_key", Class: ImportOperator, Action: ImportCarry, Target: "secrets.action_link_key", Default: "", Type: String, Choices: nil, Cutover: false, Secret: true},
	{Module: "action_links", Key: "phone_actions_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.phone_actions_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false},
	{Module: "action_links", Key: "phone_actions_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.phone_actions_kinds", Default: "reminder,triage_label,triage_archive,triage_snooze,memory,profile", Type: String, Choices: nil, Cutover: false},
	{Module: "anthropic", Key: "anthropic_api_key", Class: ImportOperator, Action: ImportRename, Target: "service/anthropic credential api_key", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_effort_cron", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_effort_cron", Default: "high", Type: Enum, Choices: []string{"low", "medium", "high", "xhigh", "max"}, Cutover: false},
	{Module: "assistant", Key: "agent_effort_nightly", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_effort_nightly", Default: "high", Type: Enum, Choices: []string{"low", "medium", "high", "xhigh", "max"}, Cutover: false},
	{Module: "assistant", Key: "agent_effort_subagent", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_effort_subagent", Default: "medium", Type: Enum, Choices: []string{"low", "medium", "high", "xhigh", "max"}, Cutover: false},
	{Module: "assistant", Key: "agent_file_cap_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentFileCapBytes", Default: "33554432", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_file_name_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentFileNameChars", Default: "80", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_grafana_cell_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentGrafanaCellChars", Default: "300", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_grafana_max_alerts", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentGrafanaMaxAlerts", Default: "50", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_grafana_max_dashboards", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentGrafanaMaxDashboards", Default: "20", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_grafana_max_log_rows", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentGrafanaMaxLogRows", Default: "200", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_grafana_max_metric_rows", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentGrafanaMaxMetricRows", Default: "50", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_grafana_max_series", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentGrafanaMaxSeries", Default: "20", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_grafana_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentGrafanaTimeoutSeconds", Default: "15", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_journal_lease_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentJournalLeaseSeconds", Default: "1800", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_kb_profile_tokens", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentKbProfileTokens", Default: "2000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_kb_retrieval_distance", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentKbRetrievalDistance", Default: "0.35", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_kb_retrieval_tokens", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentKbRetrievalTokens", Default: "1500", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_kb_write_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentKbWriteChars", Default: "4000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_mail_body_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentMailBodyChars", Default: "8000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_mail_max_loops_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentMailMaxLoopsLimit", Default: "100", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_mail_max_results", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentMailMaxResults", Default: "20", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_mail_snippet_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentMailSnippetChars", Default: "300", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_manifest_results", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentManifestResults", Default: "30", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_media_torrent_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentMediaTorrentLimit", Default: "50", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_message_cap_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentMessageCapBytes", Default: "67108864", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_nightly_max_threads", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentNightlyMaxThreads", Default: "30", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_nightly_thread_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentNightlyThreadChars", Default: "3000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_nightly_total_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentNightlyTotalChars", Default: "40000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_preview_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentPreviewChars", Default: "600", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_project_instruction_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentProjectInstructionChars", Default: "20000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_project_name_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentProjectNameChars", Default: "80", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_recall_max_page_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentRecallMaxPageChars", Default: "16000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_recall_page_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentRecallPageChars", Default: "8000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_ref_tokens", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentRefTokens", Default: "8000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_calendar_candidates", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_calendar_candidates", Default: "0 * * * *", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_daily_nudges", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_daily_nudges", Default: "daily 08:00", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_knowledge_export", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_knowledge_export", Default: "daily 03:30", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_max_active_jobs", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/scheduler/tuning.go:TuningAgentScheduleMaxActiveJobs", Default: "20", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_media_channels", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_media_channels", Default: "*/30 * * * *", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_media_sweep", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_media_sweep", Default: "*/10 * * * *", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_media_thumbnails", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_media_thumbnails", Default: "daily 04:00", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_memory_work", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_memory_work", Default: "*/2 * * * *", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_min_interval_seconds", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_min_interval_seconds", Default: "3600", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_nightly", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_nightly", Default: "daily 02:30", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_ops_sampler", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_ops_sampler", Default: "*/15 * * * *", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_package_tracking", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_package_tracking", Default: "0 * * * *", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_task_reminders", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_task_reminders", Default: "*/5 * * * *", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_schedule_weekly_review", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_schedule_weekly_review", Default: "weekly sun 18:00", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_scheduler_lease_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/scheduler/tuning.go:TuningAgentSchedulerLeaseSeconds", Default: "1800", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_scheduler_missed_grace_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/scheduler/tuning.go:TuningAgentSchedulerMissedGraceSeconds", Default: "120", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_scheduler_tick_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/scheduler/tuning.go:TuningAgentSchedulerTickSeconds", Default: "30", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_stub_tokens", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentStubTokens", Default: "2000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_context_ids", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentSubagentContextIds", Default: "20", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_description_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentSubagentDescriptionChars", Default: "160", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_digest_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentSubagentDigestChars", Default: "6000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_max_concurrent", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentSubagentMaxConcurrent", Default: "2", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_max_cost_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.agent_subagent_max_cost_usd", Default: "0.25", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_max_steps", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentSubagentMaxSteps", Default: "12", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_prompt_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentSubagentPromptChars", Default: "4000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_subagent_task_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentSubagentTaskChars", Default: "4000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_web_search_max_uses", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWebSearchMaxUses", Default: "5", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_webfetch_max_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWebfetchMaxBytes", Default: "2097152", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_webfetch_max_redirects", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWebfetchMaxRedirects", Default: "5", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_webfetch_page_text_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWebfetchPageTextChars", Default: "60000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_webfetch_text_default", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWebfetchTextDefault", Default: "12000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_webfetch_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWebfetchTimeoutSeconds", Default: "10", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_webfetch_user_agent", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWebfetchUserAgent", Default: "atrium-webfetch/1", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_workspace_plan_item_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWorkspacePlanItemChars", Default: "200", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "agent_workspace_plan_items", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/tuning.go:TuningAgentWorkspacePlanItems", Default: "20", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "anthropic_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/anthropic.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "anthropic_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/anthropic.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "budget_chat_share", Class: ImportOperator, Action: ImportCarry, Target: "settings.budget_chat_share", Default: "1", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "budget_unattended_share", Class: ImportOperator, Action: ImportCarry, Target: "settings.budget_unattended_share", Default: "0.8", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "cache_ttl_chat", Class: ImportOperator, Action: ImportCarry, Target: "settings.cache_ttl_chat", Default: "5m", Type: Enum, Choices: []string{"5m", "1h"}, Cutover: false},
	{Module: "assistant", Key: "default_context_limit", Class: ImportOperator, Action: ImportCarry, Target: "settings.default_context_limit", Default: "90000", Type: Enum, Choices: []string{"90000", "200000", "500000", "1000000"}, Cutover: false},
	{Module: "assistant", Key: "default_effort", Class: ImportOperator, Action: ImportCarry, Target: "settings.default_effort", Default: "xhigh", Type: Enum, Choices: []string{"low", "medium", "high", "xhigh", "max"}, Cutover: false},
	{Module: "assistant", Key: "default_model", Class: ImportOperator, Action: ImportCarry, Target: "settings.default_model", Default: "claude-haiku-5-5", Type: Enum, Choices: []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"}, Cutover: false},
	{Module: "assistant", Key: "digest_notify", Class: ImportOperator, Action: ImportCarry, Target: "settings.digest_notify", Default: "true", Type: Bool, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "doom_loop_threshold", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/loop/tuning.go:TuningDoomLoopThreshold", Default: "3", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "dreaming_max_cost_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.dreaming_max_cost_usd", Default: "0.30", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "dreaming_monthly_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.dreaming_monthly_usd", Default: "3", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "max_run_cost_usd_haiku_5_5", Class: ImportOperator, Action: ImportCarry, Target: "settings.max_run_cost_usd_haiku_5_5", Default: "2", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "max_run_cost_usd_opus_5_5", Class: ImportOperator, Action: ImportCarry, Target: "settings.max_run_cost_usd_opus_5_5", Default: "10", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "max_run_cost_usd_sonnet_5_5", Class: ImportOperator, Action: ImportCarry, Target: "settings.max_run_cost_usd_sonnet_5_5", Default: "6", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "max_steps_chat", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/loop/tuning.go:TuningMaxStepsChat", Default: "24", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "max_steps_cron", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/loop/tuning.go:TuningMaxStepsCron", Default: "24", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "max_steps_nightly", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/loop/tuning.go:TuningMaxStepsNightly", Default: "24", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "max_steps_worker", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/loop/tuning.go:TuningMaxStepsWorker", Default: "8", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "monthly_budget_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.monthly_budget_usd", Default: "150", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "open_meteo_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/weather.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "open_meteo_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/weather.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "ops_condenser", Class: ImportOperator, Action: ImportCarry, Target: "settings.ops_condenser", Default: "haiku", Type: Enum, Choices: []string{"haiku", "local"}, Cutover: false},
	{Module: "assistant", Key: "ops_condenser_max_tokens", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/serverops/tuning.go:TuningOpsCondenserMaxTokens", Default: "2000", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "ops_condenser_model", Class: ImportOperator, Action: ImportCarry, Target: "settings.ops_condenser_model", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "ops_worker_max_run_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.ops_worker_max_run_usd", Default: "0.10", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "ops_worker_monthly_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.ops_worker_monthly_usd", Default: "5", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "prompt_subagent", Class: ImportOperator, Action: ImportCarry, Target: "settings.prompt_subagent", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "prompt_system", Class: ImportOperator, Action: ImportCarry, Target: "settings.prompt_system", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "thread_cost_cap_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.thread_cost_cap_usd", Default: "10", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_forecast_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/weather/tuning.go:TuningWeatherForecastDays", Default: "7", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_geocoding_url", Class: ImportHost, Action: ImportDropHost, Target: "service/weather.config.geocoding_url", Default: "https://geocoding-api.open-meteo.com/v1/search", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_hourly_hours", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/weather/tuning.go:TuningWeatherHourlyHours", Default: "24", Type: Int, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_latitude", Class: ImportOperator, Action: ImportCarry, Target: "settings.weather_latitude", Default: "", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_location_name", Class: ImportOperator, Action: ImportCarry, Target: "settings.weather_location_name", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_longitude", Class: ImportOperator, Action: ImportCarry, Target: "settings.weather_longitude", Default: "", Type: Float, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_open_meteo_url", Class: ImportHost, Action: ImportDropHost, Target: "service/weather.config.forecast_url", Default: "https://api.open-meteo.com/v1/forecast", Type: String, Choices: nil, Cutover: false},
	{Module: "assistant", Key: "weather_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/weather/tuning.go:TuningWeatherTimeoutSeconds", Default: "15", Type: Int, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_access_domains", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_access_domains", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_access_emails", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_access_emails", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_access_groups", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_access_groups", Default: "atrium_access,atrium_admin", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_api_client_id", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_api_client_id", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_client_id", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_client_id", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_client_secret", Class: ImportHost, Action: ImportRename, Target: "service/oidc host credential client_secret", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_groups_claim", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_groups_claim", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_issuer", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_issuer", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_preset", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_preset", Default: "generic", Type: Enum, Choices: []string{"generic", "pocket_id", "authelia", "authentik", "keycloak", "google", "entra"}, Cutover: false},
	{Module: "auth", Key: "oidc_recheck_minutes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/auth/oidc/tuning.go:TuningOidcRecheckMinutes", Default: "60", Type: Int, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_scopes", Class: ImportHost, Action: ImportDropHost, Target: "host.auth.oidc_scopes", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_session_idle_days", Class: ImportOperator, Action: ImportCarry, Target: "settings.oidc_session_idle_days", Default: "7", Type: Int, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_session_max_days", Class: ImportOperator, Action: ImportCarry, Target: "settings.oidc_session_max_days", Default: "30", Type: Int, Choices: nil, Cutover: false},
	{Module: "auth", Key: "oidc_wizard_key", Class: ImportOperator, Action: ImportDrop, Target: "retired", Default: "", Type: String, Choices: nil, Cutover: false, Secret: true},
	{Module: "calendar", Key: "calendar_account", Class: ImportOperator, Action: ImportCarry, Target: "settings.calendar_account", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "calendar_dir", Class: ImportHost, Action: ImportDropHost, Target: "host.calendar.calendar_dir", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "candidate_draft_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/calendar/tuning.go:TuningCandidateDraftLimit", Default: "10", Type: Int, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "candidate_scan_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/calendar/tuning.go:TuningCandidateScanLimit", Default: "50", Type: Int, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "default_event_minutes", Class: ImportOperator, Action: ImportCarry, Target: "settings.default_event_minutes", Default: "60", Type: Int, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "google_calendar_refresh_token", Class: ImportOperator, Action: ImportRename, Target: "google/default credential oauth/calendar/refresh", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "google_client_id", Class: ImportOperator, Action: ImportRename, Target: "google/default.config.client_id", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "google_oauth_client_secret", Class: ImportOperator, Action: ImportRename, Target: "google/default credential oauth/client_secret", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "icloud_apple_id", Class: ImportOperator, Action: ImportRename, Target: "icloud/<account> caldav/carddav config.apple_id", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "icloud_calendar", Class: ImportOperator, Action: ImportCarry, Target: "settings.icloud_calendar", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "max_events", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/calendar/tuning.go:TuningMaxEvents", Default: "200", Type: Int, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "max_occurrences", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/calendar/tuning.go:TuningMaxOccurrences", Default: "2000", Type: Int, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "work_days", Class: ImportOperator, Action: ImportCarry, Target: "settings.work_days", Default: "mon,tue,wed,thu,fri", Type: String, Choices: nil, Cutover: false},
	{Module: "calendar", Key: "work_hours", Class: ImportOperator, Action: ImportCarry, Target: "settings.work_hours", Default: "09:00-17:00", Type: String, Choices: nil, Cutover: false},
	{Module: "contacts", Key: "contacts_dir", Class: ImportHost, Action: ImportDropHost, Target: "host.contacts.contacts_dir", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "contacts", Key: "contacts_max_results", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/contacts/tuning.go:TuningContactsMaxResults", Default: "10", Type: Int, Choices: nil, Cutover: false},
	{Module: "core", Key: "assistant_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.assistant_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "calendar_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.calendar_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "contacts_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.contacts_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "grafana_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.grafana_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "http_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/config/tuning.go:TuningHttpTimeoutSeconds", Default: "600", Type: Int, Choices: nil, Cutover: false},
	{Module: "core", Key: "mail_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.mail_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "media_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.media_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "notifications_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.notifications_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "notion_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.notion_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false, Note: "module flag stays an operator setting"},
	{Module: "core", Key: "ops_listen", Class: ImportHost, Action: ImportDropHost, Target: "host.core.ops_listen", Default: "127.0.0.1:9180", Type: String, Choices: nil, Cutover: false},
	{Module: "core", Key: "ops_pprof", Class: ImportHost, Action: ImportDropHost, Target: "host.core.ops_pprof", Default: "false", Type: Bool, Choices: nil, Cutover: false},
	{Module: "core", Key: "otel_traces_endpoint", Class: ImportHost, Action: ImportDropHost, Target: "host.core.otel_traces_endpoint", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "core", Key: "pass_run_retention_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/passrun/tuning.go:TuningPassRunRetentionDays", Default: "90", Type: Int, Choices: nil, Cutover: false},
	{Module: "core", Key: "public_url", Class: ImportHost, Action: ImportDropHost, Target: "host.core.public_url", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "core", Key: "tz", Class: ImportOperator, Action: ImportCarry, Target: "settings.tz", Default: "America/Port_of_Spain", Type: String, Choices: nil, Cutover: false},
	{Module: "db", Key: "key", Class: ImportHost, Action: ImportDropHost, Target: "host.db.key", Default: "", Type: String, Choices: nil, Cutover: false, Secret: true},
	{Module: "export", Key: "export_notion_enabled", Class: ImportOperator, Action: ImportCarry, Target: "settings.export_notion_enabled", Default: "false", Type: Bool, Choices: nil, Cutover: false},
	{Module: "export", Key: "export_notion_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.export_notion_kinds", Default: "pii,messages,personal", Type: String, Choices: nil, Cutover: false},
	{Module: "export", Key: "export_notion_parent", Class: ImportOperator, Action: ImportCarry, Target: "settings.export_notion_parent", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "export", Key: "export_notion_private_to", Class: ImportOperator, Action: ImportCarry, Target: "settings.export_notion_private_to", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "export", Key: "export_obsidian_dir", Class: ImportHost, Action: ImportDropHost, Target: "host.export.export_obsidian_dir", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "export", Key: "export_obsidian_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.export_obsidian_kinds", Default: "pii,messages,personal", Type: String, Choices: nil, Cutover: false},
	{Module: "export", Key: "export_obsidian_private_to", Class: ImportOperator, Action: ImportCarry, Target: "settings.export_obsidian_private_to", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "grafana_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/grafana.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "grafana_logs_query", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.grafana_logs_query", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "grafana_logs_type", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.grafana_logs_type", Default: "loki", Type: Enum, Choices: []string{"loki", "victorialogs"}, Cutover: false},
	{Module: "grafana", Key: "grafana_logs_uid", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.grafana_logs_uid", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "grafana_metrics_uid", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.grafana_metrics_uid", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "grafana_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/grafana.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "grafana_url", Class: ImportHost, Action: ImportDropHost, Target: "service/grafana.config.url", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "grafana_viewer_token", Class: ImportOperator, Action: ImportRename, Target: "service/grafana credential viewer_token", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "ops_hosts", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.ops_hosts", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "ops_label_selector", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.ops_label_selector", Default: "job=\"node\"", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "ops_query_cpu", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.ops_query_cpu", Default: "100 - (avg by(instance) (rate(node_cpu_seconds_total{mode=\"idle\",{{selector}},instance=~\"{{host}}(:.*)?\"}[5m])) * 100)", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "ops_query_disk", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.ops_query_disk", Default: "(1 - node_filesystem_avail_bytes{{{selector}},instance=~\"{{host}}(:.*)?\" ,mountpoint=\"/\"} / node_filesystem_size_bytes{{{selector}},instance=~\"{{host}}(:.*)?\" ,mountpoint=\"/\"}) * 100", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "ops_query_failed_units", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.ops_query_failed_units", Default: "sum by(instance) (node_systemd_unit_state{state=\"failed\",{{selector}},instance=~\"{{host}}(:.*)?\"} == 1)", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "ops_query_mem", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.ops_query_mem", Default: "(1 - node_memory_MemAvailable_bytes{{{selector}},instance=~\"{{host}}(:.*)?\"} / node_memory_MemTotal_bytes{{{selector}},instance=~\"{{host}}(:.*)?\"}) * 100", Type: String, Choices: nil, Cutover: false},
	{Module: "grafana", Key: "ops_query_up", Class: ImportHost, Action: ImportDropHost, Target: "host.grafana.ops_query_up", Default: "up{{{selector}},instance=~\"{{host}}(:.*)?\"}", Type: String, Choices: nil, Cutover: false},
	{Module: "mail", Key: "birthdays_default_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningBirthdaysDefaultDays", Default: "14", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "birthdays_max_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningBirthdaysMaxDays", Default: "366", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "deletion_interval_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningDeletionIntervalSeconds", Default: "86400", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "deletions_min_deleted_share", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningDeletionsMinDeletedShare", Default: "0.7", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "deletions_min_group", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningDeletionsMinGroup", Default: "5", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "deletions_otp_min_share", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningDeletionsOtpMinShare", Default: "0.8", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "deletions_otp_older_than_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningDeletionsOtpOlderThanDays", Default: "7", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "drafts_max_body", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningDraftsMaxBody", Default: "100000", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "drafts_max_subject", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningDraftsMaxSubject", Default: "300", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "folder_min_messages", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningFolderMinMessages", Default: "10", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "gmail_labels_max_messages", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningGmailLabelsMaxMessages", Default: "10", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "gmail_labels_min_overlap", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningGmailLabelsMinOverlap", Default: "0.5", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "gmail_labels_min_target_messages", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningGmailLabelsMinTargetMessages", Default: "10", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "google_gmail_refresh_token", Class: ImportOperator, Action: ImportRename, Target: "google/default credential oauth/gmail/refresh", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "mail", Key: "knn_k", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningKnnK", Default: "7", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "knn_max", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningKnnMax", Default: "4096", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "knn_min_share", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningKnnMinShare", Default: "0.9", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "llm_body_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/endpoint/tuning.go:TuningLlmBodyChars", Default: "1500", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "llm_neighbors", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningLlmNeighbors", Default: "10", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "llm_top_senders", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/endpoint/tuning.go:TuningLlmTopSenders", Default: "3", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_accounts_file", Class: ImportHost, Action: ImportDropHost, Target: "connections mail/<id>", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "mail", Key: "mail_backoff_max_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailBackoffMaxSeconds", Default: "300", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_backoff_start_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailBackoffStartSeconds", Default: "5", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_body_cap_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailBodyCapBytes", Default: "65536", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_chat_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "host.local_chat.ca_file", Default: "", Type: String, Choices: nil, Cutover: false, Note: "local-chat exception; no chat connection is created"},
	{Module: "mail", Key: "mail_chat_key", Class: ImportHost, Action: ImportCarry, Target: "settings.mail_chat_key", Default: "", Type: String, Choices: nil, Cutover: false, Secret: true, Note: "local-chat exception; no chat connection is created"},
	{Module: "mail", Key: "mail_chat_max_tokens", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/endpoint/tuning.go:TuningMailChatMaxTokens", Default: "80", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_chat_model", Class: ImportHost, Action: ImportDropHost, Target: "host.local_chat.model", Default: "qwen", Type: String, Choices: nil, Cutover: false, Note: "local-chat exception; no chat connection is created"},
	{Module: "mail", Key: "mail_chat_proxy", Class: ImportHost, Action: ImportDropHost, Target: "host.local_chat.proxy", Default: "", Type: String, Choices: nil, Cutover: false, Note: "local-chat exception; no chat connection is created"},
	{Module: "mail", Key: "mail_chat_url", Class: ImportHost, Action: ImportDropHost, Target: "host.local_chat.url", Default: "", Type: String, Choices: nil, Cutover: false, Note: "local-chat exception; no chat connection is created"},
	{Module: "mail", Key: "mail_embed_batch", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/endpoint/tuning.go:TuningMailEmbedBatch", Default: "16", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_embed_body_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/endpoint/tuning.go:TuningMailEmbedBodyChars", Default: "1500", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_embed_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/embedding.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_embed_key", Class: ImportHost, Action: ImportRename, Target: "service/embedding host credential bearer", Default: "", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_embed_model", Class: ImportHost, Action: ImportDropHost, Target: "service/embedding.config.model", Default: "embeddinggemma", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_embed_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/embedding.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_embed_url", Class: ImportHost, Action: ImportDropHost, Target: "service/embedding.config.url", Default: "", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_fetch_batch", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailFetchBatch", Default: "40", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_flag_batch", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailFlagBatch", Default: "500", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_fts_body_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailFtsBodyChars", Default: "20000", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_idle_settle_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailIdleSettleSeconds", Default: "2", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_mode", Class: ImportOperator, Action: ImportCarry, Target: "settings.mail_mode", Default: "shadow", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_scan_sample_subjects", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailScanSampleSubjects", Default: "5", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_scan_top_labels", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailScanTopLabels", Default: "5", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_search_branch_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailSearchBranchLimit", Default: "50", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_search_max_terms", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailSearchMaxTerms", Default: "12", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_search_rrf_k", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailSearchRrfK", Default: "60", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_search_snippet_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailSearchSnippetChars", Default: "220", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_search_snippet_tokens", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailSearchSnippetTokens", Default: "24", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_ship24_api_key", Class: ImportOperator, Action: ImportRename, Target: "service/ship24 credential api_key", Default: "", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_ship24_destination_country", Class: ImportOperator, Action: ImportCarry, Target: "settings.mail_ship24_destination_country", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "mail", Key: "mail_ship24_destination_postcode", Class: ImportOperator, Action: ImportCarry, Target: "settings.mail_ship24_destination_postcode", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "mail", Key: "mail_ship24_monthly_quota", Class: ImportOperator, Action: ImportCarry, Target: "settings.mail_ship24_monthly_quota", Default: "10", Type: Int, Choices: nil, Cutover: false},
	{Module: "mail", Key: "mail_ship24_poll_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tracking/tuning.go:TuningMailShip24PollDays", Default: "30", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_ship24_poll_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tracking/tuning.go:TuningMailShip24PollLimit", Default: "40", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_ship24_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tracking/tuning.go:TuningMailShip24TimeoutSeconds", Default: "15", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mail_ship24_url", Class: ImportHost, Action: ImportDropHost, Target: "service/ship24.config.url", Default: "https://api.ship24.com/public/v1", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "mailops_batch", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningMailopsBatch", Default: "100", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "openloops_commitment_scan_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningOpenloopsCommitmentScanLimit", Default: "20", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "openloops_commitment_window_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningOpenloopsCommitmentWindowDays", Default: "14", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "openloops_commitments_per_message", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningOpenloopsCommitmentsPerMessage", Default: "5", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "openloops_owe_max_age_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningOpenloopsOweMaxAgeDays", Default: "45", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "openloops_remind_ahead_days", Class: ImportOperator, Action: ImportCarry, Target: "settings.openloops_remind_ahead_days", Default: "1", Type: Int, Choices: nil, Cutover: false},
	{Module: "mail", Key: "openloops_remind_hour", Class: ImportOperator, Action: ImportCarry, Target: "settings.openloops_remind_hour", Default: "9", Type: Int, Choices: nil, Cutover: false},
	{Module: "mail", Key: "openloops_remind_overdue_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningOpenloopsRemindOverdueDays", Default: "14", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "openloops_snooze_default_days", Class: ImportOperator, Action: ImportCarry, Target: "settings.openloops_snooze_default_days", Default: "3", Type: Int, Choices: nil, Cutover: false},
	{Module: "mail", Key: "openloops_waiting_after_days", Class: ImportOperator, Action: ImportCarry, Target: "settings.openloops_waiting_after_days", Default: "3", Type: Int, Choices: nil, Cutover: false},
	{Module: "mail", Key: "openloops_waiting_max_age_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningOpenloopsWaitingMaxAgeDays", Default: "60", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "openloops_window_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningOpenloopsWindowDays", Default: "120", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_cluster_iterations", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceClusterIterations", Default: "15", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_cluster_ks", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceClusterKs", Default: "2,3,4", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_cluster_margin", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceClusterMargin", Default: "0.04", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_cluster_min_embedded", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceClusterMinEmbedded", Default: "0.8", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_merge_min_group", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceMergeMinGroup", Default: "3", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_merge_min_others", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceMergeMinOthers", Default: "10", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_merge_min_share", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceMergeMinShare", Default: "0.9", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_min_folder", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceMinFolder", Default: "40", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_min_group", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceMinGroup", Default: "20", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_min_share", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceMinShare", Default: "0.1", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_name_max_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceNameMaxChars", Default: "40", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_run_gap_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceRunGapSeconds", Default: "1209600", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_runs_required", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceRunsRequired", Default: "2", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rebalance_top_terms", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRebalanceTopTerms", Default: "5", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "report_disagreement_lines", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningReportDisagreementLines", Default: "40", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "report_top_dests", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningReportTopDests", Default: "15", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rule_min_n", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRuleMinN", Default: "3", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "rule_min_purity", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningRuleMinPurity", Default: "0.9", Type: Float, Choices: nil, Cutover: true},
	{Module: "mail", Key: "sent_backfill_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningSentBackfillDays", Default: "90", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "ship24_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/ship24.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "ship24_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/ship24.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: true},
	{Module: "mail", Key: "triage_top_dests", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningTriageTopDests", Default: "15", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "uncertain_dest", Class: ImportOperator, Action: ImportCarry, Target: "settings.uncertain_dest", Default: "Misc", Type: String, Choices: nil, Cutover: false},
	{Module: "mail", Key: "watches_body_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningWatchesBodyChars", Default: "3000", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "watches_carrier_domains", Class: ImportOperator, Action: ImportCarry, Target: "settings.watches_carrier_domains", Default: "ups.com,fedex.com,usps.com,dhl.com,usps.gov,dhl.de", Type: String, Choices: nil, Cutover: false},
	{Module: "mail", Key: "watches_package_window_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningWatchesPackageWindowDays", Default: "60", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "watches_scan_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningWatchesScanLimit", Default: "25", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "watches_stale_package_days", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningWatchesStalePackageDays", Default: "60", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "write_attempts", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/mail/tuning.go:TuningWriteAttempts", Default: "3", Type: Int, Choices: nil, Cutover: true},
	{Module: "mail", Key: "write_cap", Class: ImportOperator, Action: ImportCarry, Target: "settings.write_cap", Default: "100", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "link_key", Class: ImportOperator, Action: ImportCarry, Target: "secrets.link_key", Default: "", Type: String, Choices: nil, Cutover: false, Secret: true},
	{Module: "media", Key: "media_artwork_miss_ttl_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaArtworkMissTtlSeconds", Default: "604800", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_artwork_show_search_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaArtworkShowSearchLimit", Default: "5", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_channel_name_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaChannelNameChars", Default: "120", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_channel_pattern_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaChannelPatternChars", Default: "300", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_channel_seen_ttl_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaChannelSeenTtlSeconds", Default: "7776000", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_child_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaChildLimit", Default: "500", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_eztv_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaEztvLimit", Default: "100", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_eztv_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaEztvTimeoutSeconds", Default: "30", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_eztv_url", Class: ImportHost, Action: ImportDropHost, Target: "service/eztv.config.url", Default: "https://eztvx.to/api/get-torrents", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_ffmpeg_nice", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaFfmpegNice", Default: "10", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_ffmpeg_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaFfmpegTimeoutSeconds", Default: "120", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_link_ttl_seconds", Class: ImportOperator, Action: ImportCarry, Target: "settings.media_link_ttl_seconds", Default: "43200", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_movies_url", Class: ImportHost, Action: ImportDropHost, Target: "host.media.media_movies_url", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_qbittorrent_api_key", Class: ImportOperator, Action: ImportRename, Target: "service/qbittorrent credential api_key", Default: "", Type: String, Choices: nil, Cutover: true, Runtime: true},
	{Module: "media", Key: "media_qbittorrent_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaQbittorrentTimeoutSeconds", Default: "15", Type: Int, Choices: nil, Cutover: true, Runtime: true},
	{Module: "media", Key: "media_qbittorrent_url", Class: ImportHost, Action: ImportDropHost, Target: "service/qbittorrent.config.url", Default: "", Type: String, Choices: nil, Cutover: true, Runtime: true},
	{Module: "media", Key: "media_root", Class: ImportHost, Action: ImportDropHost, Target: "host.media.media_root", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_search_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaSearchLimit", Default: "50", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_subtitle_extensions", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaSubtitleExtensions", Default: ".srt,.sub,.idx,.ass,.ssa,.vtt", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_thumbnail_pass_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaThumbnailPassLimit", Default: "200", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_thumbnail_position", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaThumbnailPosition", Default: "0.1", Type: Float, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_thumbnail_width", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaThumbnailWidth", Default: "480", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_tmdb_images_url", Class: ImportHost, Action: ImportDropHost, Target: "service/tmdb.config.images_url", Default: "https://image.tmdb.org/t/p/w342", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_tmdb_search_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaTmdbSearchLimit", Default: "8", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_tmdb_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaTmdbTimeoutSeconds", Default: "15", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_tmdb_token", Class: ImportOperator, Action: ImportRename, Target: "service/tmdb credential read_token", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_tmdb_url", Class: ImportHost, Action: ImportDropHost, Target: "service/tmdb.config.url", Default: "https://api.themoviedb.org/3", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_torrent_upload_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaTorrentUploadBytes", Default: "1048576", Type: Int, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_tv_url", Class: ImportHost, Action: ImportDropHost, Target: "host.media.media_tv_url", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "media_video_extensions", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/media/tuning.go:TuningMediaVideoExtensions", Default: ".mkv,.mp4,.m4v,.avi,.mov,.ts,.wmv", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "qbittorrent_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/qbittorrent.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: true, Runtime: true},
	{Module: "media", Key: "qbittorrent_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/qbittorrent.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: true, Runtime: true},
	{Module: "media", Key: "tmdb_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/tmdb.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "media", Key: "tmdb_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/tmdb.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "memory", Key: "memory_concurrency", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/memory/tuning.go:TuningMemoryConcurrency", Default: "4", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "memory", Key: "memory_effort", Class: ImportOperator, Action: ImportCarry, Target: "settings.memory_effort", Default: "low", Type: String, Choices: nil, Cutover: false},
	{Module: "memory", Key: "memory_model", Class: ImportOperator, Action: ImportCarry, Target: "settings.memory_model", Default: "claude-haiku-5-5", Type: String, Choices: nil, Cutover: false},
	{Module: "memory", Key: "memory_node_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/memory/tuning.go:TuningMemoryNodeBytes", Default: "512", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "memory", Key: "memory_tail_messages", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/memory/tuning.go:TuningMemoryTailMessages", Default: "6", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "memory", Key: "memory_verbatim_tokens", Class: ImportOperator, Action: ImportCarry, Target: "settings.memory_verbatim_tokens", Default: "16000", Type: Int, Choices: nil, Cutover: false},
	{Module: "memory", Key: "memory_view_high", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/memory/tuning.go:TuningMemoryViewHigh", Default: "128000", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "memory", Key: "memory_view_low", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/memory/tuning.go:TuningMemoryViewLow", Default: "64000", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notify", Key: "notify_body_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notify/tuning.go:TuningNotifyBodyChars", Default: "1000", Type: Int, Choices: nil, Cutover: false},
	{Module: "notify", Key: "notify_nightly_cap", Class: ImportOperator, Action: ImportCarry, Target: "settings.notify_nightly_cap", Default: "1", Type: Int, Choices: nil, Cutover: false},
	{Module: "notify", Key: "notify_per_run_cap", Class: ImportOperator, Action: ImportCarry, Target: "settings.notify_per_run_cap", Default: "3", Type: Int, Choices: nil, Cutover: false},
	{Module: "notify", Key: "notify_title_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notify/tuning.go:TuningNotifyTitleChars", Default: "120", Type: Int, Choices: nil, Cutover: false},
	{Module: "notify", Key: "webpush_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/webpush.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "notify", Key: "webpush_subject", Class: ImportOperator, Action: ImportRename, Target: "service/webpush.config.subject", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "notify", Key: "webpush_vapid_private_key", Class: ImportOperator, Action: ImportRename, Target: "service/webpush credential vapid_private", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "notion", Key: "notion_block_depth", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionBlockDepth", Default: "5", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_branch_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionBranchLimit", Default: "50", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_ca_file", Class: ImportHost, Action: ImportDropHost, Target: "service/notion.transport.ca_file", Default: "", Type: String, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_head_source_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionHeadSourceChars", Default: "1000", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_label_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionLabelChars", Default: "48", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_max_aliases", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionMaxAliases", Default: "20", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_max_retries", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionMaxRetries", Default: "5", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_max_sources", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionMaxSources", Default: "12", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_page_size", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionPageSize", Default: "100", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_private_value", Class: ImportOperator, Action: ImportCarry, Target: "settings.notion_private_value", Default: "private", Type: String, Choices: nil, Cutover: false},
	{Module: "notion", Key: "notion_proxy", Class: ImportHost, Action: ImportDropHost, Target: "service/notion.transport.proxy", Default: "", Type: String, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_related_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionRelatedLimit", Default: "10", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_retry_after_default_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionRetryAfterDefaultSeconds", Default: "1", Type: Float, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_retry_after_max_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionRetryAfterMaxSeconds", Default: "60", Type: Float, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_snippet_chars", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionSnippetChars", Default: "220", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_timeout_seconds", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/notion/tuning.go:TuningNotionTimeoutSeconds", Default: "60", Type: Int, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "notion_token", Class: ImportOperator, Action: ImportRename, Target: "service/notion credential token", Default: "", Type: String, Choices: nil, Cutover: false, Runtime: true},
	{Module: "notion", Key: "sensitivity_property", Class: ImportOperator, Action: ImportCarry, Target: "settings.sensitivity_property", Default: "sensitivity", Type: String, Choices: nil, Cutover: false},
	{Module: "notion", Key: "wiki_log_title", Class: ImportOperator, Action: ImportCarry, Target: "settings.wiki_log_title", Default: "Wiki Log", Type: String, Choices: nil, Cutover: false},
	{Module: "notion", Key: "wiki_root", Class: ImportOperator, Action: ImportCarry, Target: "settings.wiki_root", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "permissions", Key: "permission_internet_clean", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_internet_clean", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_internet_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_internet_kinds", Default: "ask", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_internet_pii", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_internet_pii", Default: "deny", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_internet_untrusted_isolated", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_internet_untrusted_isolated", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_internet_untrusted_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_internet_untrusted_kinds", Default: "deny", Type: Enum, Choices: []string{"ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_internet_untrusted_knowledge", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_internet_untrusted_knowledge", Default: "ask", Type: Enum, Choices: []string{"ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_notify_clean", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_notify_clean", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_notify_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_notify_kinds", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_notify_pii", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_notify_pii", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_notify_untrusted_isolated", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_notify_untrusted_isolated", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_notify_untrusted_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_notify_untrusted_kinds", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_notify_untrusted_knowledge", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_notify_untrusted_knowledge", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_own_clean", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_own_clean", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_own_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_own_kinds", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_own_pii", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_own_pii", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_own_untrusted_isolated", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_own_untrusted_isolated", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_own_untrusted_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_own_untrusted_kinds", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_own_untrusted_knowledge", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_own_untrusted_knowledge", Default: "allow", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_people_clean", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_people_clean", Default: "ask", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_people_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_people_kinds", Default: "ask", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_people_pii", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_people_pii", Default: "ask", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_people_untrusted_isolated", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_people_untrusted_isolated", Default: "ask", Type: Enum, Choices: []string{"allow", "ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_people_untrusted_kinds", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_people_untrusted_kinds", Default: "ask", Type: Enum, Choices: []string{"ask", "deny"}, Cutover: false},
	{Module: "permissions", Key: "permission_people_untrusted_knowledge", Class: ImportOperator, Action: ImportCarry, Target: "settings.permission_people_untrusted_knowledge", Default: "ask", Type: Enum, Choices: []string{"ask", "deny"}, Cutover: false},
	{Module: "web", Key: "body_cap_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/config/tuning.go:TuningBodyCapBytes", Default: "1048576", Type: Int, Choices: nil, Cutover: false},
	{Module: "web", Key: "chat_thread_limit", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/config/tuning.go:TuningChatThreadLimit", Default: "50", Type: Int, Choices: nil, Cutover: false},
	{Module: "web", Key: "preview_bytes", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/config/tuning.go:TuningPreviewBytes", Default: "200000", Type: Int, Choices: nil, Cutover: false},
	{Module: "web", Key: "snooze_choices", Class: ImportOperator, Action: ImportCarry, Target: "settings.snooze_choices", Default: "1,3,7,14", Type: String, Choices: nil, Cutover: false},
	{Module: "web", Key: "trusted_proxy_cidrs", Class: ImportHost, Action: ImportDropHost, Target: "host.web.trusted_proxy_cidrs", Default: "", Type: String, Choices: nil, Cutover: false},
	{Module: "web", Key: "web_page_size", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/config/tuning.go:TuningWebPageSize", Default: "50", Type: Int, Choices: nil, Cutover: false},
	{Module: "web", Key: "web_run_history", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/config/tuning.go:TuningWebRunHistory", Default: "30", Type: Int, Choices: nil, Cutover: false},
	{Module: "web_search", Key: "search_web_dynamic_filtering", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/registry/tuning.go:TuningSearchWebDynamicFiltering", Default: "false", Type: Bool, Choices: nil, Cutover: false},
	{Module: "web_search", Key: "search_web_max_cost_usd", Class: ImportOperator, Action: ImportCarry, Target: "settings.search_web_max_cost_usd", Default: "0.15", Type: Float, Choices: nil, Cutover: false},
	{Module: "web_search", Key: "search_web_max_searches", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/subagents/websearch/tuning.go:TuningSearchWebMaxSearches", Default: "5", Type: Int, Choices: nil, Cutover: false},
	{Module: "web_search", Key: "search_web_max_steps", Class: ImportTuning, Action: ImportDropTuning, Target: "internal/assistant/subagents/websearch/tuning.go:TuningSearchWebMaxSteps", Default: "8", Type: Int, Choices: nil, Cutover: false},
	{Module: "web_search", Key: "search_web_model", Class: ImportOperator, Action: ImportCarry, Target: "settings.search_web_model", Default: "claude-haiku-5-5", Type: Enum, Choices: []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"}, Cutover: false},
}

var runtimeCutover = []string{
	"google_client_id",
	"google_oauth_client_secret",
	"google_calendar_refresh_token",
	"google_gmail_refresh_token",
	"anthropic_api_key",
	"anthropic_ca_file",
	"anthropic_proxy",
	"grafana_viewer_token",
	"grafana_url",
	"grafana_ca_file",
	"grafana_proxy",
	"grafana_logs_query",
	"grafana_logs_type",
	"grafana_logs_uid",
	"grafana_metrics_uid",
	"open_meteo_ca_file",
	"open_meteo_proxy",
	"weather_forecast_days",
	"weather_geocoding_url",
	"weather_hourly_hours",
	"weather_open_meteo_url",
	"weather_timeout_seconds",
	"ops_condenser_max_tokens",
	"doom_loop_threshold",
	"max_steps_chat",
	"max_steps_cron",
	"max_steps_nightly",
	"max_steps_worker",
	"agent_file_cap_bytes",
	"agent_file_name_chars",
	"agent_grafana_cell_chars",
	"agent_grafana_max_alerts",
	"agent_grafana_max_dashboards",
	"agent_grafana_max_log_rows",
	"agent_grafana_max_metric_rows",
	"agent_grafana_max_series",
	"agent_grafana_timeout_seconds",
	"agent_journal_lease_seconds",
	"agent_kb_profile_tokens",
	"agent_kb_retrieval_distance",
	"agent_kb_retrieval_tokens",
	"agent_kb_write_chars",
	"agent_mail_body_chars",
	"agent_mail_max_loops_limit",
	"agent_mail_max_results",
	"agent_mail_snippet_chars",
	"agent_manifest_results",
	"agent_media_torrent_limit",
	"agent_message_cap_bytes",
	"agent_nightly_max_threads",
	"agent_nightly_thread_chars",
	"agent_nightly_total_chars",
	"agent_preview_chars",
	"agent_project_instruction_chars",
	"agent_project_name_chars",
	"agent_recall_max_page_chars",
	"agent_recall_page_chars",
	"agent_ref_tokens",
	"agent_schedule_max_active_jobs",
	"agent_scheduler_lease_seconds",
	"agent_scheduler_missed_grace_seconds",
	"agent_scheduler_tick_seconds",
	"agent_stub_tokens",
	"agent_subagent_context_ids",
	"agent_subagent_description_chars",
	"agent_subagent_digest_chars",
	"agent_subagent_max_concurrent",
	"agent_subagent_max_steps",
	"agent_subagent_prompt_chars",
	"agent_subagent_task_chars",
	"agent_web_search_max_uses",
	"agent_webfetch_max_bytes",
	"agent_webfetch_max_redirects",
	"agent_webfetch_page_text_chars",
	"agent_webfetch_text_default",
	"agent_webfetch_timeout_seconds",
	"agent_webfetch_user_agent",
	"agent_workspace_plan_item_chars",
	"agent_workspace_plan_items",
	"search_web_dynamic_filtering",
	"search_web_max_searches",
	"search_web_max_steps",
	"media_artwork_miss_ttl_seconds",
	"media_artwork_show_search_limit",
	"media_channel_name_chars",
	"media_channel_pattern_chars",
	"media_channel_seen_ttl_seconds",
	"media_child_limit",
	"media_eztv_limit",
	"media_eztv_timeout_seconds",
	"media_eztv_url",
	"media_ffmpeg_nice",
	"media_ffmpeg_timeout_seconds",
	"media_search_limit",
	"media_subtitle_extensions",
	"media_thumbnail_pass_limit",
	"media_thumbnail_position",
	"media_thumbnail_width",
	"media_tmdb_images_url",
	"media_tmdb_search_limit",
	"media_tmdb_timeout_seconds",
	"media_tmdb_token",
	"media_tmdb_url",
	"media_torrent_upload_bytes",
	"media_video_extensions",
	"tmdb_ca_file",
	"tmdb_proxy",
	"notify_title_chars",
	"notify_body_chars",
	"webpush_subject",
	"webpush_vapid_private_key",
	"webpush_ca_file",
	"oidc_preset",
	"oidc_issuer",
	"oidc_client_id",
	"oidc_api_client_id",
	"oidc_scopes",
	"oidc_groups_claim",
	"oidc_access_groups",
	"oidc_access_emails",
	"oidc_access_domains",
	"oidc_client_secret",
	"oidc_recheck_minutes",
	"birthdays_default_days",
	"birthdays_max_days",
	"deletion_interval_seconds",
	"deletions_min_deleted_share",
	"deletions_min_group",
	"deletions_otp_min_share",
	"deletions_otp_older_than_days",
	"drafts_max_body",
	"drafts_max_subject",
	"folder_min_messages",
	"gmail_labels_max_messages",
	"gmail_labels_min_overlap",
	"gmail_labels_min_target_messages",
	"knn_k",
	"knn_max",
	"knn_min_share",
	"llm_body_chars",
	"llm_neighbors",
	"llm_top_senders",
	"mail_backoff_max_seconds",
	"mail_backoff_start_seconds",
	"mail_body_cap_bytes",
	"mail_chat_max_tokens",
	"mail_embed_batch",
	"mail_embed_body_chars",
	"mail_embed_ca_file",
	"mail_embed_key",
	"mail_embed_model",
	"mail_embed_proxy",
	"mail_embed_url",
	"mail_fetch_batch",
	"mail_flag_batch",
	"mail_fts_body_chars",
	"mail_idle_settle_seconds",
	"mail_scan_sample_subjects",
	"mail_scan_top_labels",
	"mail_search_branch_limit",
	"mail_search_max_terms",
	"mail_search_rrf_k",
	"mail_search_snippet_chars",
	"mail_search_snippet_tokens",
	"mail_ship24_api_key",
	"mail_ship24_poll_days",
	"mail_ship24_poll_limit",
	"mail_ship24_timeout_seconds",
	"mail_ship24_url",
	"mailops_batch",
	"openloops_commitment_scan_limit",
	"openloops_commitment_window_days",
	"openloops_commitments_per_message",
	"openloops_owe_max_age_days",
	"openloops_remind_overdue_days",
	"openloops_waiting_max_age_days",
	"openloops_window_days",
	"rebalance_cluster_iterations",
	"rebalance_cluster_ks",
	"rebalance_cluster_margin",
	"rebalance_cluster_min_embedded",
	"rebalance_merge_min_group",
	"rebalance_merge_min_others",
	"rebalance_merge_min_share",
	"rebalance_min_folder",
	"rebalance_min_group",
	"rebalance_min_share",
	"rebalance_name_max_chars",
	"rebalance_run_gap_seconds",
	"rebalance_runs_required",
	"rebalance_top_terms",
	"report_disagreement_lines",
	"report_top_dests",
	"rule_min_n",
	"rule_min_purity",
	"sent_backfill_days",
	"ship24_ca_file",
	"ship24_proxy",
	"triage_top_dests",
	"watches_body_chars",
	"watches_package_window_days",
	"watches_scan_limit",
	"watches_stale_package_days",
	"write_attempts",
}

func init() {
	for _, key := range runtimeCutover {
		for i := range importManifest {
			if importManifest[i].Key == key {
				importManifest[i].Runtime = true
				break
			}
		}
	}
}

func Manifest() []ImportEntry {
	out := make([]ImportEntry, len(importManifest))
	for i, e := range importManifest {
		e.Choices = slices.Clone(e.Choices)
		out[i] = e
	}
	return out
}

var retiredImport = map[string]ImportEntry{
	"money_since":        {Key: "money_since", Class: ImportOperator, Action: ImportDrop, Target: "retired"},
	"oidc_access_group":  {Key: "oidc_access_group", Module: "auth", Class: ImportHost, Action: ImportDrop, Target: "host.auth.oidc_access_groups"},
	"oidc_admin_group":   {Key: "oidc_admin_group", Module: "auth", Class: ImportHost, Action: ImportDrop, Target: "host.auth.oidc_access_groups"},
	"oidc_admin_emails":  {Key: "oidc_admin_emails", Module: "auth", Class: ImportHost, Action: ImportDrop, Target: "host.auth.oidc_access_emails"},
	"oidc_admin_domains": {Key: "oidc_admin_domains", Module: "auth", Class: ImportHost, Action: ImportDrop, Target: "host.auth.oidc_access_domains"},
}

func RemovedHostKey(key string) (string, bool) {
	entry, ok := retiredImport[key]
	if !ok || entry.Module != "auth" {
		return "", false
	}
	return entry.Target, true
}

func ImportEntryFor(key string) (ImportEntry, bool) {
	for _, e := range importManifest {
		if e.Key == key {
			e.Choices = slices.Clone(e.Choices)
			return e, true
		}
	}
	if e, ok := retiredImport[key]; ok {
		return e, true
	}
	return ImportEntry{}, false
}

func ImportSecretName(name string) bool {
	entry, ok := ImportEntryFor(name)
	if !ok {
		return false
	}
	return entry.Secret || entry.Action == ImportRename && strings.Contains(entry.Target, "credential")
}

func ImportV2(ctx context.Context, tx *sql.Tx, src *sql.DB, pins map[string]ImportPin) ([]string, ImportSummary, error) {
	rows, err := src.QueryContext(ctx, "SELECT key, value FROM settings ORDER BY key")
	if err != nil {
		return nil, ImportSummary{}, err
	}
	type row struct{ key, value string }
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.value); err != nil {
			_ = rows.Close()
			return nil, ImportSummary{}, err
		}
		found = append(found, r)
	}
	if err := rows.Close(); err != nil {
		return nil, ImportSummary{}, err
	}
	ctx = audit.With(ctx, "import", audit.Migration)
	var lines []string
	var summary ImportSummary
	seen := make(map[string]bool, len(found))
	for _, r := range found {
		seen[r.key] = true
		entry, ok := ImportEntryFor(r.key)
		if !ok {
			if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", r.key); err != nil {
				return nil, summary, err
			}
			lines = append(lines, "setting "+r.key+": drop-unknown")
			summary.DropUnknown++
			continue
		}
		pin, pinned := pins[r.key]
		if ImportSecretName(r.key) && pinned {
			if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", r.key); err != nil {
				return nil, summary, err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Key: r.key, Secret: true}); err != nil {
				return nil, summary, err
			}
			lines = append(lines, "setting "+r.key+": drop-shadowed-row (the effective pin wins)")
			lines = append(lines, "pin "+r.key+": credential-file-binding "+entry.Target)
			summary.Dropped++
			continue
		}
		switch {
		case r.key == "mail_mode" && r.value == "live":
			if _, err := tx.ExecContext(ctx, "UPDATE settings SET value = 'shadow' WHERE key = 'mail_mode'"); err != nil {
				return nil, summary, err
			}
			old, next := "live", "shadow"
			if err := audit.Record(ctx, tx, audit.Entry{Key: "mail_mode", Old: &old, New: &next}); err != nil {
				return nil, summary, err
			}
			lines = append(lines, "setting mail_mode: reset-for-cutover live -> shadow")
			summary.ResetShadow++
		case entry.Action == ImportCarry:
			line := "setting " + r.key + ": carried -> " + entry.Target
			if pinned {
				if _, err := tx.ExecContext(ctx, "UPDATE settings SET value = ? WHERE key = ?", pin.Value, r.key); err != nil {
					return nil, summary, err
				}
				line += " from pin " + pin.Source
			}
			if entry.Note != "" {
				line += "; " + entry.Note
			}
			lines = append(lines, line)
			summary.Carried++
		case entry.Action == ImportRename:
			if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", r.key); err != nil {
				return nil, summary, err
			}
			line := "setting " + r.key + ": renamed -> " + entry.Target
			if pinned {
				line += " from pin " + pin.Source
			}
			lines = append(lines, line)
			summary.Renamed++
		case entry.Action == ImportDrop:
			if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", r.key); err != nil {
				return nil, summary, err
			}
			line := "setting " + r.key + ": dropped -> " + entry.Target
			if entry.Secret {
				line += " (transient state; value not kept)"
				if err := audit.Record(ctx, tx, audit.Entry{Key: r.key, Secret: true}); err != nil {
					return nil, summary, err
				}
			}
			if entry.Class == ImportHost {
				line += hostRequirement(entry, r.value, pin, pinned)
			}
			lines = append(lines, line)
			summary.Dropped++
		case entry.Action == ImportDropHost:
			if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", r.key); err != nil {
				return nil, summary, err
			}
			line := "setting " + r.key + ": drop-host -> " + entry.Target + hostRequirement(entry, r.value, pin, pinned)
			if entry.Note != "" {
				line += "; " + entry.Note
			}
			lines = append(lines, line)
			summary.DropHost++
		case entry.Action == ImportDropTuning:
			if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", r.key); err != nil {
				return nil, summary, err
			}
			lines = append(lines, tuningLine("setting "+r.key+": drop-tuning -> "+entry.Target, entry, r.value, pin, pinned))
			summary.DropTuning++
		default:
			lines = append(lines, "setting "+r.key+": unmapped-operator-choice -> "+entry.Target)
			summary.UnmappedOperator++
		}
	}
	for _, key := range slices.Sorted(maps.Keys(pins)) {
		if seen[key] {
			continue
		}
		pin := pins[key]
		entry, ok := ImportEntryFor(key)
		if !ok {
			lines = append(lines, "pin "+key+": unknown-host-requirement")
			continue
		}
		if ImportSecretName(key) {
			lines = append(lines, "pin "+key+": credential-file-binding "+entry.Target)
			continue
		}
		switch entry.Action {
		case ImportCarry:
			if _, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, pin.Value); err != nil {
				return nil, summary, err
			}
			line := "pin " + key + ": carried -> " + entry.Target + " from pin " + pin.Source
			if entry.Note != "" {
				line += "; " + entry.Note
			}
			lines = append(lines, line)
			summary.Carried++
		case ImportRename:
			lines = append(lines, "pin "+key+": renamed -> "+entry.Target+" from pin "+pin.Source)
			summary.Renamed++
		case ImportDrop:
			lines = append(lines, "pin "+key+": dropped -> "+entry.Target)
			summary.Dropped++
		case ImportDropHost:
			lines = append(lines, "pin "+key+": drop-host -> "+entry.Target+hostRequirement(entry, "", pin, true))
			summary.DropHost++
		case ImportDropTuning:
			lines = append(lines, tuningLine("pin "+key+": drop-tuning -> "+entry.Target, entry, "", pin, true))
			summary.DropTuning++
		default:
			lines = append(lines, "pin "+key+": unmapped-operator-choice -> "+entry.Target)
			summary.UnmappedOperator++
		}
	}
	if summary.UnmappedOperator != 0 {
		return nil, summary, fmt.Errorf("db import-v2: %d operator choices have no import action; nothing was imported", summary.UnmappedOperator)
	}
	return lines, summary, nil
}

func hostRequirement(entry ImportEntry, stored string, pin ImportPin, pinned bool) string {
	if entry.Secret {
		return "; host requirement " + entry.Target + " (secret; value not printed)"
	}
	value, origin := stored, "settings"
	if pinned {
		value, origin = pin.Value, "pin "+pin.Source
	}
	if value == "" {
		value = "unset"
	}
	return "; host requirement " + entry.Target + " = " + value + " (" + origin + ")"
}

func tuningLine(prefix string, entry ImportEntry, stored string, pin ImportPin, pinned bool) string {
	value, origin := stored, "source"
	if pinned {
		value, origin = pin.Value, "pin "+pin.Source
	}
	if value == entry.Default {
		return prefix
	}
	return prefix + "; " + origin + " value " + value + " differs from the compiled default " + entry.Default + " (changed-from-default)"
}
