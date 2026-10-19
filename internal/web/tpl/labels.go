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
		{"re", "Python regular expression, case-insensitive"},
		{"gte", "Whole number of days"},
	},
	"kind": {
		{"rebalance_split", "Split folder"},
		{"rebalance_merge", "Merge folders"},
		{"deletion_rule", "Deletion rule"},
		{"gmail_label_merge", "Merge labels"},
		{"inbox_backlog", "Inbox backlog"},
		{"notion_link", "Notion link"},
		{"notion_edit", "Notion edit"},
		{"calendar_event", "Calendar event"},
		{"reminder", "Reminder"},
		{"notion_wiki_plan", "Wiki plan"},
	},
	"kind_icon": {
		{"rebalance_split", "split"},
		{"rebalance_merge", "merge"},
		{"deletion_rule", "trash-2"},
		{"gmail_label_merge", "tag"},
		{"inbox_backlog", "inbox"},
		{"notion_link", "link"},
		{"notion_edit", "pencil"},
		{"calendar_event", "calendar"},
		{"reminder", "scroll-text"},
		{"notion_wiki_plan", "book-open"},
	},
	"status": {
		{"pending", "Pending"},
		{"approved", "Applying"},
		{"applied", "Applied"},
		{"failed", "Failed"},
		{"dismissed", "Dismissed"},
		{"suppressed", "Dismissed forever"},
	},
}

var badgeSets = map[string]map[string]Badge{
	"action": {
		"applied":     {"secondary", "Applied"},
		"skipped":     {"outline", "Skipped"},
		"undone":      {"ghost", "Undone"},
		"interrupted": {"outline", "Interrupted"},
	},
	"run": {
		"running":     {"outline", "Working"},
		"interrupted": {"destructive", "Interrupted"},
		"failed":      {"destructive", "Failed"},
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
