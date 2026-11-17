package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestKnowledgeSelectedRecordsConfirmTogether(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	browser := webG3(t, base)

	var ids []int64
	for _, title := range []string{"Boiler code", "Boiler service", "Boiler brand"} {
		created := okG3(t, "create knowledge record", browser.do(http.MethodPost, "/web/v1/knowledge", map[string]any{"title": title, "body": title + " details.", "scope": "work", "marks": []string{"untrusted"}}))
		data, _ := created["data"].(map[string]any)
		ids = append(ids, number(t, data["id"]))
	}
	okG3(t, "knowledge list", browser.do(http.MethodGet, "/web/v1/knowledge", nil))
	confirmed := okG3(t, "confirm selected", browser.do(http.MethodPost, "/web/v1/knowledge/confirm", map[string]any{"ids": []int64{ids[0], ids[1]}}))
	if data, _ := confirmed["data"].(map[string]any); data == nil || len(data["committed"].([]any)) != 2 {
		t.Fatalf("confirm selected = %v, want two committed records", confirmed)
	}
	for i, want := range []bool{true, true, false} {
		if _, confirmed, _ := knowledgeMarks(t, d, ids[i]); confirmed != want {
			t.Errorf("record %d confirmed = %v, want %v", ids[i], confirmed, want)
		}
	}
	if got := browser.do(http.MethodPost, "/web/v1/knowledge/confirm", map[string]any{"ids": []int64{}}); got.status < 400 {
		t.Errorf("confirming nothing = %d %s, want a refusal", got.status, got.body)
	}
	if _, confirmed, _ := knowledgeMarks(t, d, ids[2]); confirmed {
		t.Error("an empty selection confirmed a record")
	}
}

func TestAskBarCarriesWhatTheOperatorAskedAbout(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
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
}
