package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/settings"
)

func TestServePhoneCalendarApprovalDoesNotBorrowCLIActor(t *testing.T) {
	dir, _ := davFixture(t)
	seedSettings(t, map[string]string{policy.EnabledKey: "true", policy.KindsKey: "calendar_delete"})
	id := proposalID(t, toolResult(t, "calendar_delete", map[string]any{"uid": "untracked"}))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var fingerprint string
	if err := d.Read.QueryRow("SELECT fingerprint FROM proposals WHERE id = ?", id).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ctx := settings.With(context.Background(), fixtureFor(t).settings)
	approve, _, err := actionlink.MintPair(ctx, d.Write, pid, fingerprint, float64(time.Now().UnixNano())/1e9)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := startServeStoppable(t, dir)
	code, body, _ := fetch(t, &http.Client{}, http.MethodPost, base+"/actions/"+approve, nil)
	if code != http.StatusOK || strings.Contains(body, `"code"`) {
		t.Fatalf("phone approval = %d: %s", code, body)
	}
	var status, result string
	if err := d.Read.QueryRow("SELECT status, json_extract(result, '$.error') FROM proposals WHERE id = ?", id).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || result == "" || strings.HasPrefix(result, "{") {
		t.Fatalf("phone approval result = %s %q", status, result)
	}
	var actor, source string
	if err := d.Read.QueryRow("SELECT actor, source FROM operation_audit WHERE operation = 'calendar.request-delete' ORDER BY id DESC LIMIT 1").Scan(&actor, &source); err != nil {
		t.Fatal(err)
	}
	if actor == "operator" && source == "cli" {
		t.Fatalf("public phone request borrowed %s/%s", actor, source)
	}
}

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

func TestCalendarProposalAuditKeepsIDWhenCandidateDraftFails(t *testing.T) {
	dir, _ := davFixture(t)
	asstWarm(t)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	asstExec(t, d, "INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	res, err := d.Write.Exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1, 1)")
	if err != nil {
		t.Fatal(err)
	}
	mailbox, _ := res.LastInsertId()
	res, err = d.Write.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, first_seen) VALUES ('icloud', ?, 1, 1, 'event@test', 'event', 'Dinner', 'friend@example.com', 0)", mailbox)
	if err != nil {
		t.Fatal(err)
	}
	message, _ := res.LastInsertId()
	asstExec(t, d, "INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (?, 'invite', 'queued', 1)", message)
	asstExec(t, d, "CREATE TRIGGER refuse_candidate_draft BEFORE UPDATE ON event_candidates BEGIN SELECT RAISE(ABORT, 'candidate storage unavailable'); END")
	toolResult(t, "event_propose", map[string]any{
		"calendar": "icloud", "title": "Dinner", "start": "2026-11-10T12:00:00", "source_message_id": message,
	})
	var id, audited int64
	var status, outcome, target string
	if err := d.Read.QueryRow("SELECT id, status FROM proposals ORDER BY id DESC LIMIT 1").Scan(&id, &status); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT outcome, target, json_extract(details_json, '$.proposal_id') FROM operation_audit WHERE operation = 'calendar.propose-create' ORDER BY id DESC LIMIT 1").Scan(&outcome, &target, &audited); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || outcome != "failed" || target != "proposal:"+strconv.FormatInt(id, 10) || audited != id {
		t.Fatalf("proposal %d %s audit = %s %q proposal %d", id, status, outcome, target, audited)
	}
}
