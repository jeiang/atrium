package tpl

import "testing"

func TestThreadLabels(t *testing.T) {
	badges := map[string]map[string]Badge{
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
	for set, table := range badges {
		for key, want := range table {
			if got := badge(set, key); got == nil || *got != want {
				t.Errorf("badge(%s, %q) = %v, want %+v", set, key, got, want)
			}
		}
		if got := badge(set, "weird"); got != nil {
			t.Errorf("badge(%s, weird) = %+v, want nil", set, got)
		}
	}
	labels := map[string]map[string]string{
		"thread_kind": {
			"chat": "Chat", "cron": "Scheduled run", "nightly": "Nightly run", "sub": "Research run", "subagent": "Subagent",
		},
		"file_origin": {"upload": "Uploaded", "generated": "Generated"},
	}
	for set, table := range labels {
		for key, want := range table {
			if got := lbl(set, key); got != want {
				t.Errorf("lbl(%s, %q) = %v, want %q", set, key, got, want)
			}
		}
		if got := lbl(set, "weird"); got != "weird" {
			t.Errorf("lbl(%s, weird) = %v, want raw key", set, got)
		}
	}
}
