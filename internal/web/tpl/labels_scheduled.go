package tpl

func init() {
	badgeSets["job_run"] = map[string]Badge{
		"running":          {"outline", "Running"},
		"ok":               {"secondary", "Completed"},
		"error":            {"destructive", "Failed"},
		"skipped_budget":   {"outline", "Skipped (budget)"},
		"skipped_pause":    {"outline", "Skipped (paused)"},
		"missed_coalesced": {"secondary", "Ran late"},
	}
	labelSets["job_run_hint"] = []pair{
		{"skipped_budget", "The monthly budget was reached, so this run did not start."},
		{"skipped_pause", "Live mode was paused, so this run did not start."},
		{"missed_coalesced", "The run was due while atrium was down; missed runs were merged into one."},
	}
	labelSets["job_source"] = []pair{
		{"nightly", "Nightly"},
		{"builtin", "Built-in"},
		{"operator", "Created by you"},
		{"agent", "Created by the assistant"},
	}
}
