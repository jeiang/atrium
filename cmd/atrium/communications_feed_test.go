package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/unit"
)

const feedReadersWant = `1|Power bill 1|0
2|Power bill 2|0
91|Invitation: Dentist appointment|1
calendar_event|calendar|pending|Add to calendar: Dentist appointment (2027-10-20 14:00)|{"calendar":"icloud","spec":{"uid":"dentist-20271020@clinic.example","title":"Dentist appointment","start":"2027-10-20T14:00:00+00:00","end":"2027-10-20T15:00:00+00:00","all_day":false,"location":"Clinic Room 2","notes":"Routine check-up"},"source":{"from":"calendar@clinic.example","invitation":true,"message_id":91,"subject":"Invitation: Dentist appointment"}}|{"calendar":"icloud","title":"Dentist appointment","start":"2027-10-20T14:00:00+00:00","end":"2027-10-20T15:00:00+00:00","all_day":false,"location":"Clinic Room 2","notes":"Routine check-up","source":{"from":"calendar@clinic.example","invitation":true,"message_id":91,"subject":"Invitation: Dentist appointment"}}
event calls: Power bill 1, Power bill 2
`

func TestMailPassProposesEventsThroughTheFeed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, loadGoldenData(t), nil, "")
	path := filepath.Join(f.dir, "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	invite := filepath.Join(f.dir, "cache", "raw", "icloud", "1", "7_7.eml")
	raw, err := os.ReadFile(invite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invite, []byte(strings.NewReplacer("20261020T", "20271020T", "dentist-20261020@", "dentist-20271020@").Replace(string(raw))), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := "account = 'icloud' AND (subject LIKE 'Invitation:%' OR id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 2))"
	var before int64
	for _, q := range []string{
		"UPDATE messages SET seen = 1 WHERE account = 'icloud'",
		"UPDATE messages SET seen = 0, sent = 0, in_inbox = 1, gone_at = NULL, first_seen = (SELECT applied_at FROM schema_migrations WHERE version = 8) + 1 WHERE " + targets,
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Read.QueryRow("SELECT COALESCE(MAX(id), 0) FROM proposals").Scan(&before); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []string
	f.api.SetOnRequest(func(req anthropictest.Request) {
		body := req.JSON(t)
		if callerOf(t, body) == "event" {
			mu.Lock()
			events = append(events, subjectIn(body))
			mu.Unlock()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if code, _, stderr := f.Run(ctx, "sync", "--account", "icloud"); code != cli.ExitOK {
		t.Fatalf("sync: exit %d %q", code, stderr)
	}
	var got strings.Builder
	dump := func(query string, args ...any) {
		t.Helper()
		rows, err := d.Read.Query(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
			cols, err := r.Columns()
			if err != nil {
				return "", err
			}
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				return "", err
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = v.String
			}
			return strings.Join(parts, "|"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			got.WriteString(line + "\n")
		}
	}
	dump("SELECT s.message_id, m.subject, s.found FROM mail_scans s JOIN messages m ON m.id = s.message_id WHERE s.kind = 'events' ORDER BY s.message_id")
	dump("SELECT kind, account, status, title, payload, preview FROM proposals WHERE id > ? ORDER BY id", before)
	mu.Lock()
	slices.Sort(events)
	fmt.Fprintf(&got, "event calls: %s\n", strings.Join(events, ", "))
	mu.Unlock()
	if got.String() != feedReadersWant {
		t.Errorf("feed readers output changed:\n%s", got.String())
	}
}

func TestFeedReadersAreEmptyWithMailOff(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept(unit.Mail, "false"))
	d := asstDB(t, dir)
	asstExec(t, d, "INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	asstExec(t, d, "INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1, 1)")
	asstExec(t, d, "INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, first_seen) VALUES ('icloud', 1, 1, 1, 'lease@test', 'lease', 'Your lease offer', 'agent@example.com', 0)")
	asstExec(t, d, "INSERT INTO tasks (id, title, source_kind, source_id, created_ts, updated_ts) VALUES (40, 'Sign the lease', 'message', '1', 1, 1)")
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	task := apiData(t, s.do(http.MethodGet, "/api/v1/tasks/40", token, nil), http.StatusOK)["task"].(map[string]any)
	links, _ := task["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("links with mail off = %v", task["links"])
	}
	if l := links[0].(map[string]any); l["kind"] != "message" || l["id"] != "1" || l["title"] != nil {
		t.Fatalf("message link with mail off = %v, want no title", l)
	}
	expectField(t, s.do(http.MethodPost, "/api/v1/tasks", token, map[string]any{"title": "Reply", "link": map[string]any{"kind": "message", "id": "1"}}), "link")
}
