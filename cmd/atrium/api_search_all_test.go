package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func searchGroups(t *testing.T, data map[string]any) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	groups, _ := data["groups"].([]any)
	for _, g := range groups {
		group := g.(map[string]any)
		hits := []map[string]any{}
		for _, item := range group["items"].([]any) {
			hit := item.(map[string]any)
			if hit["type"] == "record" && hit["title"] == "Standing instructions" {
				continue
			}
			if hit["type"] != group["type"] {
				t.Fatalf("hit %v in group %v", hit, group["type"])
			}
			snippet := utf16.Encode([]rune(hit["snippet"].(string)))
			for _, m := range hit["matches"].([]any) {
				span := m.(map[string]any)
				start, end := int(number(t, span["start"])), int(number(t, span["end"]))
				if start < 0 || end > len(snippet) || start >= end {
					t.Fatalf("match %v outside snippet %q", span, hit["snippet"])
				}
				if got := strings.ToLower(string(utf16.Decode(snippet[start:end]))); !strings.HasPrefix(got, "statement") && got != "tomatoes" {
					t.Fatalf("match %v marks %q in %q", span, got, hit["snippet"])
				}
			}
			hits = append(hits, hit)
		}
		out[group["type"].(string)] = hits
	}
	return out
}

func TestSearchAllAPI(t *testing.T) {
	t.Parallel()
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mrvMailFixture(t, "shadow")
	if got := runApp(t, "sync"); got.code != 0 {
		t.Fatalf("sync: %s", got.stderr)
	}
	d := mustConnect(t, filepath.Join(m.dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	asstExec(t, d, "INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (7001, 'chat', 'Quarter review', 'low', 10, 10)")
	asstExec(t, d, "INSERT INTO thread_fts (thread_id, text) VALUES (7001, 'we compared the statement totals')")
	asstExec(t, d, "INSERT INTO thread_fts (thread_id, text) VALUES (7001, 'the statement balance matched')")
	asstExec(t, d, "INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (7002, 'chat', 'Garden', 'low', 11, 11)")
	asstExec(t, d, "INSERT INTO thread_fts (thread_id, text) VALUES (7002, 'tomatoes and basil')")
	asstExec(t, d, "INSERT INTO kb_records (id, title, body, scope, writer, created_at, updated_at) VALUES (8001, 'Bank statement', 'The monthly statement arrives on the first.', 'personal', 'operator', 20, 20)")
	asstExec(t, d, "INSERT INTO kb_records (id, title, body, scope, writer, private, created_at, updated_at) VALUES (8002, 'Salary', 'The salary statement is kept apart.', 'personal', 'operator', 1, 21, 21)")
	asstExec(t, d, "INSERT INTO tasks (title, notes, status, created_ts, updated_ts) VALUES ('File the statement', '', 'open', 30, 30)")
	asstExec(t, d, "INSERT INTO tasks (title, notes, status, created_ts, updated_ts) VALUES ('Call the bank', 'ask why the statement is late', 'open', 31, 31)")
	asstExec(t, d, "INSERT INTO thread_files (thread_id, name, media_type, path, origin, created_at) VALUES (7001, 'statement-march.pdf', 'application/pdf', 'statement-march.pdf', 'upload', 40)")
	asstExec(t, d, "INSERT INTO thread_files (name, media_type, path, origin, created_at) VALUES ('statement-loose.pdf', 'application/pdf', 'statement-loose.pdf', 'upload', 41)")
	asstExec(t, d, "INSERT INTO projects (id, name, effort, context_limit, created_at, updated_at) VALUES (9001, 'Taxes', 'low', 100000, 5, 5)")
	asstExec(t, d, "INSERT INTO thread_files (id, project_id, name, media_type, path, origin, created_at) VALUES (9101, 9001, 'statement-april.pdf', 'application/pdf', 'statement-april.pdf', 'upload', 39)")
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, m.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	s := &apiSite{t: t, base: base}
	find := func(query string) map[string]any {
		t.Helper()
		return apiData(t, s.do(http.MethodGet, "/api/v1/search/all?"+query, token, nil), http.StatusOK)
	}
	state := func() string {
		t.Helper()
		var revisions string
		if err := d.Read.QueryRow("SELECT COALESCE(group_concat(scope || '=' || value, ','), '') FROM (SELECT scope, value FROM read_revisions WHERE scope IN ('assistant', 'knowledge', 'tasks', 'mail') ORDER BY scope)").Scan(&revisions); err != nil {
			t.Fatal(err)
		}
		var counts string
		if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM threads) || '/' || (SELECT COUNT(*) FROM kb_records) || '/' || (SELECT COUNT(*) FROM tasks) || '/' || (SELECT COUNT(*) FROM messages) || '/' || (SELECT COUNT(*) FROM thread_files) || '/' || (SELECT COUNT(*) FROM operation_audit)").Scan(&counts); err != nil {
			t.Fatal(err)
		}
		return revisions + " " + counts
	}

	expectAPI(t, s.do(http.MethodGet, "/api/v1/search/all?q=statement", "", nil), http.StatusUnauthorized, "unauthenticated")
	before := state()
	for settle := time.Now().Add(15 * time.Second); ; before = state() {
		time.Sleep(500 * time.Millisecond)
		if state() == before {
			break
		}
		if time.Now().After(settle) {
			t.Fatalf("startup writes did not settle: %s", before)
		}
	}
	all := find("q=statement")
	groups := searchGroups(t, all)
	for _, kind := range []string{"thread", "message", "record", "task", "file"} {
		if len(groups[kind]) == 0 {
			t.Fatalf("no %s hit: %v", kind, all)
		}
	}
	if partial := all["partial"].([]any); len(partial) != 0 {
		t.Fatalf("partial on a healthy search: %v", partial)
	}
	if len(groups["thread"]) != 1 || number(t, groups["thread"][0]["id"]) != 7001 || groups["thread"][0]["path"] != "/threads/7001" {
		t.Fatalf("thread hits: %v", groups["thread"])
	}
	if msg := groups["message"][0]; msg["title"] != "Statement" || msg["detail"] != "pay@bank.test" || !strings.HasPrefix(msg["path"].(string), "/mail/messages/") {
		t.Fatalf("message hit: %v", msg)
	}
	private := map[int64]bool{}
	for _, r := range groups["record"] {
		private[number(t, r["id"])] = r["private"].(bool)
	}
	if len(private) != 2 || private[8001] || !private[8002] || groups["record"][0]["path"] != "/knowledge/8001" {
		t.Fatalf("record hits: %v", groups["record"])
	}
	if len(groups["task"]) != 2 || groups["task"][0]["title"] != "File the statement" {
		t.Fatalf("task hits: %v", groups["task"])
	}
	if len(groups["file"]) != 2 || groups["file"][0]["path"] != fmt.Sprintf("/threads/7001/files/%d", number(t, groups["file"][0]["id"])) || groups["file"][0]["detail"] != "Quarter review" {
		t.Fatalf("file hits: %v", groups["file"])
	}
	if groups["file"][1]["path"] != "/knowledge/spaces/9001/files/9101" || groups["file"][1]["detail"] != "Taxes" {
		t.Fatalf("space file hit: %v", groups["file"][1])
	}
	top := all["top"].([]any)
	if len(top) != 5 {
		t.Fatalf("top hits: %v", top)
	}
	var topTypes []string
	for _, h := range top {
		topTypes = append(topTypes, h.(map[string]any)["type"].(string))
	}
	if got := strings.Join(topTypes, ","); got != "message,file,task,record,thread" {
		t.Fatalf("top order %s", got)
	}

	only := find("q=statement&types=message")
	if g := searchGroups(t, only); len(g) != 1 || len(g["message"]) != 1 || len(only["top"].([]any)) != 0 {
		t.Fatalf("message filter: %v", only)
	}
	pair := searchGroups(t, find("q=statement&types="+url.QueryEscape("record,thread")))
	if len(pair) != 2 || len(pair["record"]) != 2 || len(pair["thread"]) != 1 {
		t.Fatalf("record and thread filter: %v", pair)
	}
	if got := searchGroups(t, find("q=tomatoes")); len(got["thread"]) != 1 || number(t, got["thread"][0]["id"]) != 7002 || len(got["message"]) != 0 {
		t.Fatalf("narrow query: %v", got)
	}

	first := find("q=statement&types=task&limit=1")
	cursor, _ := first["next_cursor"].(string)
	page := searchGroups(t, first)["task"]
	if len(page) != 1 || cursor == "" {
		t.Fatalf("first task page: %v", first)
	}
	second := find("q=statement&types=task&limit=1&cursor=" + url.QueryEscape(cursor))
	next := searchGroups(t, second)["task"]
	if len(next) != 1 || number(t, next[0]["id"]) == number(t, page[0]["id"]) || second["next_cursor"] != nil {
		t.Fatalf("second task page: %v", second)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/search/all?q=statement&types=message&limit=1&cursor="+url.QueryEscape(cursor), token, nil), http.StatusBadRequest, "bad_cursor")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/search/all?q=", token, nil), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/search/all?q=statement&types=calendar", token, nil), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/search/all?q=statement&limit=21", token, nil), http.StatusUnprocessableEntity, "invalid")
	if after := state(); after != before {
		t.Fatalf("search wrote: before %s after %s", before, after)
	}

	if off := runSettings(t, "set", module.FlagKey(unit.Mail), "false"); off.code != 0 {
		t.Fatalf("mail off: %+v", off)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		g := searchGroups(t, find("q=statement"))
		if _, ok := g["message"]; !ok {
			if len(g["thread"]) != 1 || len(g["record"]) != 2 {
				t.Fatalf("mail off dropped other types: %v", g)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("messages still searched with mail off: %v", g)
		}
		time.Sleep(100 * time.Millisecond)
	}

	asstExec(t, d, "DROP TABLE thread_fts")
	broken := find("q=statement")
	partial := broken["partial"].([]any)
	if len(partial) != 1 || partial[0].(map[string]any)["type"] != "thread" || partial[0].(map[string]any)["reason"] != "failed" {
		t.Fatalf("partial: %v", partial)
	}
	g := searchGroups(t, broken)
	if _, ok := g["thread"]; ok || len(g["record"]) != 2 || len(g["task"]) != 2 {
		t.Fatalf("groups beside a failed type: %v", g)
	}
	if !slices.Contains([]string{"record", "task", "file"}, broken["top"].([]any)[0].(map[string]any)["type"].(string)) {
		t.Fatalf("top beside a failed type: %v", broken["top"])
	}
}
