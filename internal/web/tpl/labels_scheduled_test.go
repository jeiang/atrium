package tpl

import "testing"

func TestScheduledLabels(t *testing.T) {
	badges := map[string]Badge{
		"running":          {"outline", "Running"},
		"ok":               {"secondary", "Completed"},
		"error":            {"destructive", "Failed"},
		"skipped_budget":   {"outline", "Skipped (budget)"},
		"skipped_pause":    {"outline", "Skipped (paused)"},
		"missed_coalesced": {"secondary", "Ran late"},
	}
	for key, want := range badges {
		if got := badge("job_run", key); got == nil || *got != want {
			t.Errorf("badge(job_run, %q) = %v, want %+v", key, got, want)
		}
	}
	if got := badge("job_run", "weird"); got != nil {
		t.Errorf("unknown badge = %+v, want nil", got)
	}

	hints := map[string]string{
		"skipped_budget":   "The monthly budget was reached, so this run did not start.",
		"skipped_pause":    "Live mode was paused, so this run did not start.",
		"missed_coalesced": "The run was due while atrium was down; missed runs were merged into one.",
	}
	for key, want := range hints {
		if got := lbl("job_run_hint", key); got != want {
			t.Errorf("lbl(job_run_hint, %q) = %v, want %q", key, got, want)
		}
	}

	sources := map[string]string{
		"nightly": "Nightly", "builtin": "Built-in", "operator": "Created by you", "agent": "Created by the assistant",
	}
	for key, want := range sources {
		if got := lbl("job_source", key); got != want {
			t.Errorf("lbl(job_source, %q) = %v, want %q", key, got, want)
		}
	}
	if got := lbl("job_source", "unknown"); got != "unknown" {
		t.Errorf("unknown source = %v, want raw key", got)
	}
}
