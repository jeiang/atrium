package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalendarAuditEntryPointsAndPlainApprovalFailures(t *testing.T) {
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
				base := startServeIn(t, dir)
				jar, _ := cookiejar.New(nil)
				client := &http.Client{Jar: jar}
				fetch(t, client, http.MethodGet, base+"/chat", nil)
				code, body := hxFetch(t, client, http.MethodPost, base+"/proposals/"+id+"/approve", url.Values{"all": {"1"}})
				if code != http.StatusOK || strings.Contains(body, `&#34;code&#34;`) {
					t.Fatalf("UI approval = %d: %s", code, body)
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
			if actor != "operator" || source != surface {
				t.Fatalf("audit = %s/%s, want operator/%s", actor, source, surface)
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
