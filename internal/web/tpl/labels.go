package tpl

type pair struct{ Key, Label string }

type Badge struct{ Variant, Label string }

var labelSets = map[string][]pair{
	"stage": {
		{"correction", "Correction"},
		{"operator", "Rule"},
		{"learned", "Learned"},
		{"knn", "kNN"},
		{"llm", "9B"},
		{"fallback", "Fallback"},
	},
	"stage_dot": {
		{"correction", "bg-chart-5"},
		{"operator", "bg-chart-4"},
		{"learned", "bg-chart-3"},
		{"knn", "bg-chart-2"},
		{"llm", "bg-chart-1"},
		{"fallback", "bg-chart-6"},
	},
	"level": {
		{"addr", "sender address"},
		{"addr+tmpl", "sender and subject template"},
		{"domain", "sender domain"},
	},
	"field": {
		{"from_addr", "From address"},
		{"from_domain", "From domain"},
		{"from_name", "From name"},
		{"reply_to", "Reply-To"},
		{"rpath_domain", "Return-Path domain"},
		{"list_id", "List-Id"},
		{"subject", "Subject"},
		{"subject_tmpl", "Subject template"},
		{"msgid_domain", "Message-ID domain"},
		{"precedence", "Precedence"},
		{"category", "Category"},
		{"to", "To"},
		{"dkim_d", "DKIM domain"},
		{"esp", "Sending service"},
		{"att_type", "Attachment type"},
		{"att_name", "Attachment name"},
		{"age_days", "Age in days"},
	},
	"op": {
		{"eq", "is"},
		{"in", "is one of"},
		{"suffix", "is or ends in domain"},
		{"re", "matches regex"},
		{"present", "is present"},
		{"absent", "is absent"},
		{"gte", "is at least"},
	},
	"op_hint": {
		{"eq", "Exact value, case-insensitive"},
		{"in", "Values separated by commas"},
		{"suffix", "Domains separated by commas"},
		{"re", "RE2 regular expression, case-insensitive"},
		{"gte", "Whole number of days"},
	},
	"kind": {
		{"notion_link", "Notion link"},
		{"notion_edit", "Notion edit"},
	},
	"kind_icon": {
		{"notion_link", "link"},
		{"notion_edit", "pencil"},
	},
	"status": {
		{"pending", "Pending"},
		{"approved", "Applying"},
		{"applied", "Applied"},
		{"failed", "Failed"},
		{"dismissed", "Dismissed"},
		{"suppressed", "Dismissed forever"},
	},
	"job_run_hint": {
		{"skipped_budget", "The monthly budget was reached, so this run did not start."},
		{"skipped_pause", "Live mode was paused, so this run did not start."},
		{"skipped_module", "The module that owns this job is disabled, so this run did not start."},
		{"missed_coalesced", "The run was due while atrium was down; missed runs were merged into one."},
	},
	"job_source": {
		{"nightly", "Nightly"},
		{"builtin", "Built-in"},
		{"operator", "Created by you"},
		{"agent", "Created by the assistant"},
	},
	"thread_kind": {
		{"chat", "Chat"},
		{"cron", "Scheduled run"},
		{"nightly", "Nightly run"},
		{"sub", "Research run"},
		{"subagent", "Subagent"},
	},
	"file_origin": {
		{"upload", "Uploaded"},
		{"generated", "Generated"},
	},
}

var badgeSets = map[string]map[string]Badge{
	"action": {
		"held":         {"outline", "Held"},
		"applied":      {"secondary", "Applied"},
		"failed":       {"destructive", "Failed"},
		"partial":      {"destructive", "Partial"},
		"skipped":      {"outline", "Skipped"},
		"unknown":      {"destructive", "Outcome unknown"},
		"interrupted":  {"outline", "Interrupted"},
		"settled":      {"outline", "Checked"},
		"undone":       {"ghost", "Undone"},
		"undo_held":    {"outline", "Undo running"},
		"undo_failed":  {"destructive", "Undo failed"},
		"undo_unknown": {"destructive", "Undo outcome unknown"},
		"undo_partial": {"destructive", "Undo partial"},
		"undo_settled": {"outline", "Undo not done"},
	},
	"run": {
		"running":     {"outline", "Working"},
		"interrupted": {"destructive", "Interrupted"},
		"failed":      {"destructive", "Failed"},
	},
	"job_run": {
		"running":          {"outline", "Running"},
		"ok":               {"secondary", "Completed"},
		"error":            {"destructive", "Failed"},
		"skipped_budget":   {"outline", "Skipped (budget)"},
		"skipped_pause":    {"outline", "Skipped (paused)"},
		"skipped_module":   {"outline", "Skipped (module off)"},
		"missed_coalesced": {"secondary", "Ran late"},
	},
	"subagent_status": {
		"queued":      {"outline", "Queued"},
		"running":     {"outline", "Running"},
		"done":        {"outline", "Done"},
		"error":       {"destructive", "Failed"},
		"interrupted": {"destructive", "Interrupted"},
	},
	"draft_status": {
		"draft":     {"outline", "Draft"},
		"sending":   {"outline", "Sending"},
		"sent":      {"secondary", "Sent"},
		"discarded": {"outline", "Discarded"},
		"unknown":   {"destructive", "Unknown"},
	},
}

func lbl(set string, key any, def ...any) any {
	for _, p := range labelSets[set] {
		if p.Key == str(key) {
			return p.Label
		}
	}
	if len(def) > 0 {
		return def[0]
	}
	return key
}

func badge(set string, key any) *Badge {
	b, ok := badgeSets[set][str(key)]
	if !ok {
		return nil
	}
	return &b
}
