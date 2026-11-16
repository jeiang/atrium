package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/unit"
)

const feedReadersWant = ``

func TestMailPassAndCandidateDraftsReadMessagesThroughTheFeed(t *testing.T) {
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
	if err := os.WriteFile(invite, []byte(strings.ReplaceAll(string(raw), "20261020T", "20271020T")), 0o600); err != nil {
		t.Fatal(err)
	}
	var before int64
	for _, q := range []string{
		"DELETE FROM event_candidates",
		"UPDATE messages SET seen = 1 WHERE account = 'icloud'",
		"UPDATE messages SET seen = 0, sent = 0, in_inbox = 1, gone_at = NULL WHERE account = 'icloud' AND (subject LIKE 'Invitation:%' OR id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 2))",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Read.QueryRow("SELECT COALESCE(MAX(id), 0) FROM proposals").Scan(&before); err != nil {
		t.Fatal(err)
	}
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
			return strings.Join(parts, " | "), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			got.WriteString(line + "\n")
		}
	}
	dump("SELECT e.message_id, m.subject, e.status, e.reason FROM event_candidates e JOIN messages m ON m.id = e.message_id ORDER BY e.message_id")
	dump("SELECT kind, account, status, title, payload, preview FROM proposals WHERE id > ? ORDER BY id", before)

	var icloud, gmail int64
	if err := d.Read.QueryRow("SELECT MIN(id) FROM messages WHERE account = 'icloud' AND id NOT IN (SELECT message_id FROM event_candidates)").Scan(&icloud); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT MIN(id) FROM messages WHERE account = 'gmail'").Scan(&gmail); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO event_candidates (message_id, reason, status, ts) VALUES (?, 'test', 'queued', 3), (?, 'test', 'queued', 2), (999999, 'gone', 'queued', 1)",
		gmail, icloud); err != nil {
		t.Fatal(err)
	}
	f.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	code, stdout, stderr := f.Run(ctx, "calendar", "candidates")
	var result struct {
		Candidates int   `json:"candidates"`
		Proposed   int   `json:"proposed"`
		RunID      int64 `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("calendar candidates: exit %d %q %q: %v", code, stdout, stderr, err)
	}
	got.WriteString(fmt.Sprintf("calendar candidates: exit %d candidates %d proposed %d\n", code, result.Candidates, result.Proposed))
	var prompt string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = (SELECT thread_id FROM runs WHERE id = ?) AND role = 'user' ORDER BY seq LIMIT 1",
		result.RunID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`Message ids: [^"\\]*`).FindString(prompt)
	got.WriteString(strings.NewReplacer(strconv.FormatInt(gmail, 10), "<gmail>", strconv.FormatInt(icloud, 10), "<icloud>").Replace(ids) + "\n")
	if got.String() != feedReadersWant {
		t.Errorf("feed readers output changed:\n%s", got.String())
	}
}

func TestFeedReadersAreEmptyWithMailOff(t *testing.T) {
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
