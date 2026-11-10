package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/notion"
)

func kbImportRun(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := cliExitProcessArgs(t, args)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return exitCode(cmd.Run()), stdout.String(), stderr.String()
}

func kbImportStats(t *testing.T, stdout string) kb.ImportStats {
	t.Helper()
	var stats kb.ImportStats
	if err := json.Unmarshal([]byte(stdout), &stats); err != nil {
		t.Fatalf("import output %q: %v", stdout, err)
	}
	return stats
}

const importSeed = `
INSERT INTO agent_profile (key, value, updated_at) VALUES ('tea', 'green', 1);
INSERT INTO nightly_observations (subject, value, first_seen, last_seen, count) VALUES
  ('tea', 'green', 100, 200, 3),
  ('tea', 'black', 50, 60, 1);
INSERT INTO notion_pages (id, parent_id, title, markdown, private, in_trash, url, last_edited_time, edited_ts) VALUES
  ('11111111-1111-1111-1111-111111111111', '00000000-0000-0000-0000-000000000001', 'Kitchen',
   '# Kitchen

Call 0412 345 678 for the plumber.

Last reviewed 2026-01-01', 0, 0, 'https://notion.invalid/kitchen', '2026-01-01T00:00:00Z', 1),
  ('22222222-2222-2222-2222-222222222222', '00000000-0000-0000-0000-000000000001', 'Private notes',
   'The safe code is 4417.', 1, 0, 'https://notion.invalid/private', '2026-01-01T00:00:00Z', 1),
  ('33333333-3333-3333-3333-333333333333', '00000000-0000-0000-0000-000000000001', 'Empty', '', 0, 0, '', '', 1);
INSERT INTO wiki_pages (page_id, title, aliases, in_trash) VALUES
  ('11111111-1111-1111-1111-111111111111', 'Kitchen', '["pantry"]', 0);
`

func TestKnowledgeImportIsIdempotent(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(notion.WikiRootKey), "00000000-0000-0000-0000-000000000001")
	if ran := runApp(t, "settings", "list"); ran.code != 0 {
		t.Fatalf("settings list: %d %s", ran.code, ran.stderr)
	}
	if code, stdout, stderr := kbImportRun(t, importSeed, "db", "shell"); code != 0 {
		t.Fatalf("seed: %d %s %s", code, stdout, stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()

	code, stdout, stderr := kbImportRun(t, "", "kb", "import", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: %d %s %s", code, stderr, stdout)
	}
	dry := kbImportStats(t, stdout)
	if !dry.DryRun || dry.Profile.Created != 1 || dry.Observations.Created != 2 || dry.Notion.Created != 2 {
		t.Errorf("dry run stats = %+v, want 1 profile, 2 observations, 2 notion pages created", dry)
	}
	var rows int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("dry run wrote %d records (%v), want none", rows, err)
	}

	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("import: %d %s %s", code, stderr, stdout)
	}
	first := kbImportStats(t, stdout)
	if first.Profile.Created != 1 || first.Observations.Created != 2 || first.Notion.Created != 2 || first.PII != 1 || first.Private != 1 {
		t.Fatalf("import stats = %+v, want 1 profile, 2 observations, 2 notion pages, 1 pii, 1 private", first)
	}
	type record struct {
		title     string
		scope     string
		marks     int64
		pinned    int64
		private   int64
		writer    string
		aliases   string
		confirmed bool
		retracted bool
	}
	load := func(origin string) (record, bool) {
		t.Helper()
		var r record
		var confirmedAt, retractedAt *float64
		err := d.Read.QueryRow("SELECT title, scope, marks, pinned, private, writer, aliases, confirmed_at, retracted_at FROM kb_records WHERE origin = ?", origin).
			Scan(&r.title, &r.scope, &r.marks, &r.pinned, &r.private, &r.writer, &r.aliases, &confirmedAt, &retractedAt)
		if err != nil {
			return record{}, false
		}
		r.confirmed, r.retracted = confirmedAt != nil, retractedAt != nil
		return r, true
	}
	profile, ok := load("profile:tea")
	if !ok || profile.title != "tea" || profile.marks != 0 || profile.pinned != 1 || !profile.confirmed || profile.writer != "import" {
		t.Errorf("profile record = %+v, want pinned, confirmed, unmarked, import", profile)
	}
	latest, ok := load("observation:tea=green")
	if !ok || latest.marks != 1 || latest.confirmed || latest.retracted {
		t.Errorf("latest observation = %+v, want untrusted and unconfirmed", latest)
	}
	older, ok := load("observation:tea=black")
	if !ok || older.marks != 1 || !older.retracted {
		t.Errorf("older observation = %+v, want untrusted and retracted", older)
	}
	kitchen, ok := load("notion:11111111-1111-1111-1111-111111111111")
	if !ok || kitchen.marks != 3 || kitchen.private != 0 || kitchen.aliases != `["pantry"]` || kitchen.confirmed {
		t.Errorf("kitchen page = %+v, want untrusted+pii, aliases and unconfirmed", kitchen)
	}
	private, ok := load("notion:22222222-2222-2222-2222-222222222222")
	if !ok || private.marks != 1 || private.private != 1 {
		t.Errorf("private page = %+v, want untrusted and private", private)
	}
	if _, ok := load("notion:33333333-3333-3333-3333-333333333333"); ok {
		t.Error("the empty page was imported")
	}

	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("second import: %d %s %s", code, stderr, stdout)
	}
	second := kbImportStats(t, stdout)
	if second.Profile.Created+second.Observations.Created+second.Notion.Created != 0 {
		t.Errorf("second import created records: %+v", second)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records").Scan(&rows); err != nil || rows != 5 {
		t.Fatalf("second import left %d records (%v), want 5", rows, err)
	}

	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	var kitchenID int64
	if err := d.Read.QueryRow("SELECT id FROM kb_records WHERE origin = 'notion:11111111-1111-1111-1111-111111111111'").Scan(&kitchenID); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d", kitchenID), url.Values{
		"title": {"Kitchen"}, "body": {"Edited in the tab."}, "scope": {"personal"},
	}, false); code != http.StatusSeeOther {
		t.Fatalf("edit in the tab = %d: %s", code, body)
	}
	if _, err := d.Write.Exec("UPDATE notion_pages SET markdown = '# Kitchen\n\nChanged in Notion.' WHERE id = '11111111-1111-1111-1111-111111111111'"); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("third import: %d %s %s", code, stderr, stdout)
	}
	third := kbImportStats(t, stdout)
	if third.Notion.Kept != 2 || len(third.ChangedInAtrium) != 1 || third.ChangedInAtrium[0] != "notion:11111111-1111-1111-1111-111111111111" {
		t.Errorf("third import = %+v, want the edited page kept and reported", third)
	}
	var body string
	if err := d.Read.QueryRow("SELECT body FROM kb_records WHERE id = ?", kitchenID).Scan(&body); err != nil || body != "Edited in the tab." {
		t.Errorf("edited record body = %q, %v; want the tab edit kept", body, err)
	}

	if _, err := d.Write.Exec("DELETE FROM agent_profile WHERE key = 'tea'"); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("fourth import: %d %s %s", code, stderr, stdout)
	}
	fourth := kbImportStats(t, stdout)
	if len(fourth.Missing) != 1 || fourth.Missing[0] != "profile:tea" {
		t.Errorf("missing sources = %v, want profile:tea", fourth.Missing)
	}
	if _, ok := load("profile:tea"); !ok {
		t.Error("the record whose source is gone was removed")
	}
}

func TestKnowledgeImportTreatsUniformPrivacyAsDefault(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(notion.WikiRootKey), "00000000-0000-0000-0000-000000000001")
	if ran := runApp(t, "settings", "list"); ran.code != 0 {
		t.Fatalf("settings list: %d %s", ran.code, ran.stderr)
	}
	seed := `
INSERT INTO notion_pages (id, parent_id, title, markdown, private, in_trash, url, last_edited_time, edited_ts) VALUES
  ('44444444-4444-4444-4444-444444444444', '00000000-0000-0000-0000-000000000001', 'Kitchen',
   'The kettle is blue.', 1, 0, 'https://notion.invalid/kitchen', '2026-01-01T00:00:00Z', 1),
  ('55555555-5555-5555-5555-555555555555', '00000000-0000-0000-0000-000000000001', 'Notes',
   'The spare key is with the neighbour.', 1, 0, 'https://notion.invalid/notes', '2026-01-01T00:00:00Z', 1);
`
	if code, stdout, stderr := kbImportRun(t, seed, "db", "shell"); code != 0 {
		t.Fatalf("seed: %d %s %s", code, stdout, stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()

	code, stdout, stderr := kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("import: %d %s %s", code, stderr, stdout)
	}
	first := kbImportStats(t, stdout)
	if first.Notion.Created != 2 || first.Private != 0 || len(first.Notes) != 1 {
		t.Fatalf("import stats = %+v, want 2 created, no private records and the uniform-privacy note", first)
	}
	loadPrivate := func(origin string) int64 {
		t.Helper()
		var private int64
		if err := d.Read.QueryRow("SELECT private FROM kb_records WHERE origin = ?", origin).Scan(&private); err != nil {
			t.Fatalf("load %s: %v", origin, err)
		}
		return private
	}
	if got := loadPrivate("notion:44444444-4444-4444-4444-444444444444"); got != 0 {
		t.Errorf("kitchen private = %d, want the sync default treated as not private", got)
	}
	if got := loadPrivate("notion:55555555-5555-5555-5555-555555555555"); got != 0 {
		t.Errorf("notes private = %d, want the sync default treated as not private", got)
	}

	if _, err := d.Write.Exec("UPDATE kb_records SET private = 1 WHERE origin LIKE 'notion:%'"); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("legacy import: %d %s %s", code, stderr, stdout)
	}
	legacy := kbImportStats(t, stdout)
	if legacy.Notion.Updated != 2 || legacy.Private != 0 || len(legacy.Notes) != 1 {
		t.Fatalf("legacy import = %+v, want both untouched records updated to not private", legacy)
	}
	if got := loadPrivate("notion:44444444-4444-4444-4444-444444444444"); got != 0 {
		t.Errorf("kitchen private after the legacy import = %d, want not private", got)
	}

	if _, err := d.Write.Exec("UPDATE notion_pages SET private = 0 WHERE id = '55555555-5555-5555-5555-555555555555'"); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("second import: %d %s %s", code, stderr, stdout)
	}
	second := kbImportStats(t, stdout)
	if second.Notion.Updated != 1 || second.Private != 1 || len(second.Notes) != 0 {
		t.Fatalf("second import = %+v, want the untouched kitchen record updated to private", second)
	}
	if got := loadPrivate("notion:44444444-4444-4444-4444-444444444444"); got != 1 {
		t.Errorf("kitchen private = %d, want an explicit private flag imported", got)
	}
	if got := loadPrivate("notion:55555555-5555-5555-5555-555555555555"); got != 0 {
		t.Errorf("notes private = %d, want not private", got)
	}
}

func TestKnowledgeImportCleansNotionMarkup(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(notion.WikiRootKey), "00000000-0000-0000-0000-000000000001")
	if ran := runApp(t, "settings", "list"); ran.code != 0 {
		t.Fatalf("settings list: %d %s", ran.code, ran.stderr)
	}
	const (
		alphaID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		betaID  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		gammaID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	)
	alphaMarkdown := "# Alpha\n\n" +
		"<table header-row=\"true\"><tr><td>Name</td><td>Value</td></tr><tr><td>a|b</td><td>line<br>two</td></tr></table>\n\n" +
		"Mention <page url=\"https://app.notion.com/Beta-" + betaID + "\">Beta</page> and [Other](https://app.notion.com/Other-" + gammaID + ").\n\n" +
		"<callout icon=\"tip\">Callout text</callout>\n\n" +
		"<details><summary>Toggle title</summary>Hidden text</details>\n\n" +
		"<foo>unknown</foo> Tom &amp; Jerry<br>after break.\n\n" +
		"```\n<table>inside code</table>\n```\n\n" +
		"[Example](https://example.com/x)\n"
	seed := fmt.Sprintf(`
INSERT INTO notion_pages (id, parent_id, title, markdown, private, in_trash, url, last_edited_time, edited_ts) VALUES
  ('%s', '00000000-0000-0000-0000-000000000001', 'Alpha', '%s', 0, 0, 'https://notion.invalid/alpha', '2026-01-01T00:00:00Z', 1),
  ('%s', '00000000-0000-0000-0000-000000000001', 'Beta', 'Beta body.', 0, 0, 'https://notion.invalid/beta', '2026-01-01T00:00:00Z', 1);
`, alphaID, strings.ReplaceAll(alphaMarkdown, "'", "''"), betaID)
	if code, stdout, stderr := kbImportRun(t, seed, "db", "shell"); code != 0 {
		t.Fatalf("seed: %d %s %s", code, stdout, stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()

	code, stdout, stderr := kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("import: %d %s %s", code, stderr, stdout)
	}
	first := kbImportStats(t, stdout)
	if first.Notion.Created != 2 {
		t.Fatalf("import stats = %+v, want 2 notion pages created", first)
	}
	type record struct {
		id          int64
		body        string
		marks       int64
		confirmedAt *float64
	}
	load := func(origin string) record {
		t.Helper()
		var r record
		if err := d.Read.QueryRow("SELECT id, body, marks, confirmed_at FROM kb_records WHERE origin = ?", origin).
			Scan(&r.id, &r.body, &r.marks, &r.confirmedAt); err != nil {
			t.Fatalf("load %s: %v", origin, err)
		}
		return r
	}
	alpha := load("notion:" + alphaID)
	beta := load("notion:" + betaID)
	cleanBody := alpha.body
	outside := strings.Replace(alpha.body, "```\n<table>inside code</table>\n```", "", 1)
	for _, want := range []string{
		"| Name | Value |",
		"| --- | --- |",
		`| a\|b | line two |`,
		fmt.Sprintf("[K%d]", beta.id),
		"> Callout text",
		"**Toggle title**",
		"Hidden text",
		"unknown",
		"Tom & Jerry\nafter break.",
		"```\n<table>inside code</table>\n```",
		"[Example](https://example.com/x)",
	} {
		if !strings.Contains(alpha.body, want) {
			t.Errorf("alpha body missing %q:\n%s", want, alpha.body)
		}
	}
	for _, bad := range []string{"<table", "<page", "<callout", "<details", "<foo", "<br", "app.notion.com", "notion.so"} {
		if strings.Contains(outside, bad) {
			t.Errorf("alpha body outside code still has %q:\n%s", bad, outside)
		}
	}
	if regexp.MustCompile(`<[A-Za-z]`).MatchString(outside) {
		t.Errorf("alpha body outside code still has a raw tag:\n%s", outside)
	}
	if alpha.marks != 1 || alpha.confirmedAt != nil {
		t.Errorf("alpha marks = %d confirmed = %v, want untrusted and unconfirmed", alpha.marks, alpha.confirmedAt)
	}

	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("second import: %d %s %s", code, stderr, stdout)
	}
	second := kbImportStats(t, stdout)
	if second.Notion.Created+second.Notion.Updated != 0 || second.Notion.Kept != 2 || len(second.ChangedInAtrium) != 0 {
		t.Errorf("second import = %+v, want everything unchanged", second)
	}

	if _, err := d.Write.Exec("UPDATE kb_records SET body = ?, marks = 0, confirmed_at = 123 WHERE id = ?", alphaMarkdown, alpha.id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO agent_writes (ts, tool, target) VALUES (1, 'kb_confirm', ?)", fmt.Sprintf("kb:%d", alpha.id)); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("third import: %d %s %s", code, stderr, stdout)
	}
	third := kbImportStats(t, stdout)
	if third.Notion.Updated != 1 || third.Notion.Kept != 1 || len(third.Notes) != 1 || !strings.Contains(third.Notes[0], "confirmed") {
		t.Errorf("third import = %+v, want the confirmed record updated and reported", third)
	}
	alpha = load("notion:" + alphaID)
	if alpha.body != cleanBody || alpha.marks != 0 || alpha.confirmedAt == nil || *alpha.confirmedAt != 123 {
		t.Errorf("confirmed record = marks %d confirmed %v, want cleaned body, no marks and the original confirmation", alpha.marks, alpha.confirmedAt)
	}
	if strings.Contains(strings.Replace(alpha.body, "```\n<table>inside code</table>\n```", "", 1), "<table") {
		t.Errorf("confirmed record body was not cleaned:\n%s", alpha.body)
	}

	if _, err := d.Write.Exec("UPDATE kb_records SET body = 'Edited in the tab.' WHERE id = ?", beta.id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO agent_writes (ts, tool, target) VALUES (1, 'kb_edit', ?)", fmt.Sprintf("kb:%d", beta.id)); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = kbImportRun(t, "", "kb", "import")
	if code != 0 {
		t.Fatalf("fourth import: %d %s %s", code, stderr, stdout)
	}
	fourth := kbImportStats(t, stdout)
	if fourth.Notion.Kept != 2 || len(fourth.ChangedInAtrium) != 1 || fourth.ChangedInAtrium[0] != "notion:"+betaID {
		t.Errorf("fourth import = %+v, want the edited record kept and reported", fourth)
	}
	if got := load("notion:" + betaID).body; got != "Edited in the tab." {
		t.Errorf("edited record body = %q, want the tab edit kept", got)
	}
}
