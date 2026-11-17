package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalendarAuditEntryPointsAndPlainApprovalFailures(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"cli", "ui", "api"} {
		t.Run(surface, func(t *testing.T) {
			dir, _ := davFixture(t)
			id := proposalID(t, toolResult(t, "calendar_delete", map[string]any{"uid": "untracked"}))
			switch surface {
			case "cli":
				got := runApp(t, "proposals", "approve", id)
				if got.code == 0 || strings.Contains(got.stdout+got.stderr, `\"code\"`) {
					t.Fatalf("CLI approval = %d %q %q", got.code, got.stdout, got.stderr)
				}
			case "ui":
				c := webG4(t, startServeIn(t, dir))
				got := c.do(http.MethodPost, "/web/v1/approvals/"+id+"/approve", nil)
				if got.status != http.StatusOK || got.json["outcome"] != "failed" {
					t.Fatalf("UI approval = %d: %s", got.status, got.body)
				}
			case "api":
				s := startAPISite(t, dir, nil)
				got := s.do(http.MethodPost, "/api/v1/approvals/"+id+"/approve", s.token("owner"), nil)
				if got.status != http.StatusOK {
					t.Fatalf("API approval = %d: %s", got.status, got.body)
				}
			}
			d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
			defer func() { _ = d.Close() }()
			var actor, source, result string
			if err := d.Read.QueryRow("SELECT actor, source FROM operation_audit WHERE operation = 'calendar.request-delete' ORDER BY id DESC LIMIT 1").Scan(&actor, &source); err != nil {
				t.Fatal(err)
			}
			want := surface
			if surface == "ui" {
				want = "api"
			}
			if actor != "operator" || source != want {
				t.Fatalf("audit = %s/%s, want operator/%s", actor, source, want)
			}
			if err := d.Read.QueryRow("SELECT json_extract(result, '$.error') FROM proposals WHERE id = ?", id).Scan(&result); err != nil {
				t.Fatal(err)
			}
			if result == "" || strings.HasPrefix(result, "{") {
				t.Fatalf("stored approval error = %q", result)
			}
		})
	}
}

func TestCalendarToolProposalAuditNamesTheProposal(t *testing.T) {
	t.Parallel()
	dir, _ := davFixture(t)
	id := proposalID(t, toolResult(t, "calendar_create", map[string]any{
		"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00",
	}))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var actor, source, target, proposal string
	if err := d.Read.QueryRow("SELECT actor, source, target, CAST(json_extract(details_json, '$.proposal_id') AS TEXT) FROM operation_audit WHERE operation = 'calendar.propose-create' ORDER BY id DESC LIMIT 1").Scan(&actor, &source, &target, &proposal); err != nil {
		t.Fatal(err)
	}
	if actor != "assistant" || source != "tool" || target != "proposal:"+id || proposal != id {
		t.Fatalf("tool proposal audit = %s/%s %q proposal %q", actor, source, target, proposal)
	}
}
