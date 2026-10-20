package tpl

func init() {
	badgeSets["subagent_status"] = map[string]Badge{
		"queued":      {"outline", "Queued"},
		"running":     {"outline", "Running"},
		"done":        {"outline", "Done"},
		"error":       {"destructive", "Failed"},
		"interrupted": {"destructive", "Interrupted"},
	}
	badgeSets["draft_status"] = map[string]Badge{
		"draft":     {"outline", "Draft"},
		"sending":   {"outline", "Sending"},
		"sent":      {"secondary", "Sent"},
		"discarded": {"outline", "Discarded"},
		"unknown":   {"destructive", "Unknown"},
	}
	labelSets["thread_kind"] = []pair{
		{"chat", "Chat"},
		{"cron", "Scheduled run"},
		{"nightly", "Nightly run"},
		{"sub", "Research run"},
		{"subagent", "Subagent"},
	}
	labelSets["file_origin"] = []pair{
		{"upload", "Uploaded"},
		{"generated", "Generated"},
	}
}
