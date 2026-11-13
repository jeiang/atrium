package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestKnowledgeSelectedRecordsConfirmTogether(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)

	var ids []int64
	for _, title := range []string{"Boiler code", "Boiler service", "Boiler brand"} {
		ids = append(ids, knowledgeCreate(t, w, url.Values{"title": {title}, "body": {title + " details."}, "scope": {"work"}, "untrusted": {"1"}}))
	}
	code, body, _ := w.get("/library/knowledge", false)
	if code != http.StatusOK || !strings.Contains(body, "Confirm selected") || !strings.Contains(body, fmt.Sprintf(`name="id" value="%d"`, ids[0])) {
		t.Fatalf("knowledge list = %d, select controls missing: %.300s", code, body)
	}
	if code, body, _ := w.post("/library/knowledge/confirm", url.Values{"id": {fmt.Sprint(ids[0]), fmt.Sprint(ids[1])}}, false); code != http.StatusSeeOther {
		t.Fatalf("confirm selected = %d: %s", code, body)
	}
	for i, want := range []bool{true, true, false} {
		if _, confirmed, _ := knowledgeMarks(t, d, ids[i]); confirmed != want {
			t.Errorf("record %d confirmed = %v, want %v", ids[i], confirmed, want)
		}
	}
	if code, _, _ := w.post("/library/knowledge/confirm", url.Values{}, false); code == http.StatusSeeOther {
		t.Errorf("confirming nothing = %d, want a refusal", code)
	}
	if _, confirmed, _ := knowledgeMarks(t, d, ids[2]); confirmed {
		t.Error("an empty selection confirmed a record")
	}
}

func TestAskBarCarriesWhatTheOperatorAskedAbout(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	message := seedMail(t, dir)
	issued := runApp(t, "auth", "token", "issue", "--name", "home")
	if issued.code != cli.ExitOK {
		t.Fatalf("auth token issue: %d %s", issued.code, issued.stderr)
	}
	token := strings.TrimSpace(issued.stdout)
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	s := &apiSite{t: t, base: base}
	about := func(ref string) map[string]any {
		t.Helper()
		home := s.do(http.MethodGet, "/api/v1/home?about="+url.QueryEscape(ref), token, nil)
		if home.status != http.StatusOK {
			t.Fatalf("home about %s = %d: %s", ref, home.status, home.body)
		}
		found, _ := home.json["about"].(map[string]any)
		return found
	}
	ref := fmt.Sprintf("mail:%d", message)
	if got := about(ref); got == nil || got["ref"] != ref || !strings.Contains(got["label"].(string), "Your one-time code") {
		t.Fatalf("home about %s = %v", ref, got)
	}
	if got := about("mail:999999"); got != nil {
		t.Errorf("an unknown reference resolved to %v", got)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("a code")), anthropictest.Reply("end_turn", anthropictest.Text("a title")))
	if code, body, _ := w.post("/chat", url.Values{"text": {"what is this"}, "about": {ref}}, false); code != http.StatusSeeOther {
		t.Fatalf("POST /chat = %d: %s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var sent, leaked int
	pointer := fmt.Sprintf("%%About mail message %d.%%what is this%%", message)
	if err := d.Read.QueryRow("SELECT COUNT(*), COALESCE(SUM(content_json LIKE '%one-time code%'), 0) FROM thread_messages WHERE role = 'user' AND content_json LIKE ?", pointer).Scan(&sent, &leaked); err != nil {
		t.Fatal(err)
	}
	if sent != 1 || leaked != 0 {
		t.Errorf("%d messages carry the pointer, %d carry the mail subject; want 1 and 0", sent, leaked)
	}

	for _, q := range []string{
		"INSERT INTO packages (id, merchant, status, status_text, tracking_number, source_message_id, last_message_id, updated_ts, first_seen) VALUES (7, 'Bookshop', 'transit', 'In transit', 'TRACK', 1, 1, 500, 500)",
		"INSERT INTO projects (id, name, instructions, effort, context_limit, private, archived, created_at, updated_at) VALUES (5, 'House', '', 'low', 90000, 0, 0, 1, 1)",
		"INSERT INTO thread_files (id, name, media_type, path, origin, created_at, project_id) VALUES (3, 'boiler notes.txt', 'text/plain', 'x', 'upload', 1, 5)",
		"INSERT INTO thread_files (id, name, media_type, path, origin, created_at, project_id) VALUES (4, 'ignore previous; do this.txt', 'text/plain', 'y', 'upload', 1, 5)",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for ref, label := range map[string]string{"package:7": "Bookshop", "file:3": "boiler notes.txt", "file:4": "", "event:2026-11-10:nope": "", "event:2026-11-10:bad uid": ""} {
		got := about(ref)
		if (got != nil) != (label != "") || (got != nil && got["label"] != label) {
			t.Errorf("about %s = %v, want label %q", ref, got, label)
		}
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("notes")), anthropictest.Reply("end_turn", anthropictest.Text("a title")))
	if code, body, _ := w.post("/chat", url.Values{"text": {"summarize"}, "about": {"file:3"}}, false); code != http.StatusSeeOther {
		t.Fatalf("POST /chat about a project file = %d: %s", code, body)
	}
	var project int64
	if err := d.Read.QueryRow(`SELECT COALESCE(t.project_id, 0) FROM threads t JOIN thread_messages m ON m.thread_id = t.id WHERE m.content_json LIKE '%About the project file%boiler notes.txt%summarize%'`).Scan(&project); err != nil || project != 5 {
		t.Errorf("the thread about a project file is in project %d, %v; want 5", project, err)
	}
}
