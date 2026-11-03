package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
)

type attentionSite struct {
	t      *testing.T
	base   string
	client *http.Client
	dir    string
}

func startAttentionSite(t *testing.T, flags map[string]string) *attentionSite {
	t.Helper()
	dir, _ := moduleFixture(t, flags)
	if ran := runApp("attention", "list"); ran.code != cli.ExitOK {
		t.Fatalf("creating the schema: exit %d stderr %q", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	attentionSeedPage(t, d, time.Now())
	_ = d.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &attentionSite{t: t, base: startServeIn(t, dir), dir: dir,
		client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *attentionSite) do(method, path string, form url.Values, htmx bool) (int, string, http.Header) {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, s.base+path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range s.client.Jar.Cookies(req.URL) {
			if c.Name == "atrium_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text), resp.Header
}

func (s *attentionSite) get(path string) string {
	s.t.Helper()
	status, body, _ := s.do("GET", path, nil, false)
	if status != http.StatusOK {
		s.t.Fatalf("GET %s = %d: %.300s", path, status, body)
	}
	return body
}

func (s *attentionSite) post(path string, form url.Values) (int, string, http.Header) {
	s.t.Helper()
	return s.do("POST", path, form, true)
}

func (s *attentionSite) exec(query string, args ...any) {
	s.t.Helper()
	d, err := db.Connect(filepath.Join(s.dir, "atrium.db"))
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec(query, args...); err != nil {
		s.t.Fatal(err)
	}
}

func (s *attentionSite) sql(query string, args ...any) []string {
	s.t.Helper()
	d, err := db.Connect(filepath.Join(s.dir, "atrium.db"))
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := d.Read.Query(query, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			s.t.Fatal(err)
		}
		parts := make([]string, len(cols))
		for i, v := range vals {
			parts[i] = fmt.Sprint(v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	return out
}

func attentionRow(page, title string) string {
	i := strings.Index(page, title)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(page[:i], "<li ")
	end := strings.Index(page[i:], "</li>")
	if start < 0 || end < 0 {
		return ""
	}
	return page[start : i+end]
}

func attentionSeedPage(t *testing.T, d *db.DB, now time.Time) {
	t.Helper()
	exec := func(query string, args ...any) {
		if _, err := d.Write.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	ts := float64(now.Add(-48 * time.Hour).Unix())
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (1, 'rebalance_split', 'pending', ?, 'Split receipts', 'f1')", ts)
	exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('t1', 'icloud', 'owe_reply', 1, ?, 'Bo', 'Lunch', 1)", ts)
	exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('t2', 'icloud', 'waiting_on_them', 2, ?, 'Cy', 'Quote', 1)", ts)
	exec("INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (1, 'icloud', 'bo@example.com', 'Re: Lunch', 'x', 'draft', ?, ?)", ts, ts)
	exec("INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (7, 'dinner on friday', 'queued', ?)", ts)
	exec("INSERT INTO commitments (id, message_id, text, owner, due, created_ts) VALUES (1, 1, 'Send the report', 'me', ?, ?)", yesterday, ts)
	exec("INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (1, 'chat', 'Trip', 'medium', ?, ?)", ts, ts)
	exec("INSERT INTO thread_plans (thread_id, items_json, updated_at) VALUES (1, '[{\"text\":\"Pick hotel\",\"done\":false}]', ?)", ts)
	for _, id := range []int{10, 11} {
		payload := fmt.Sprintf(`{"commitment_id":%d,"text":"Call the bank","due":"2031-01-15","owner":"me","message_id":1,"tz":"America/Port_of_Spain"}`, id)
		exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint, payload, preview) VALUES (?, 'reminder', 'pending', ?, ?, ?, ?, ?)",
			id, ts, fmt.Sprintf("Remind me %d", id), fmt.Sprintf("r%d", id), payload, payload)
	}
}

func TestAttentionPageListsGroupsAndOffersOnlyTheItemActions(t *testing.T) {
	s := startAttentionSite(t, flagsExcept(module.Mail, "true"))
	page := s.get("/attention")
	for _, want := range []string{
		"Send the report", "Overdue", "Split receipts", "Remind me 10", "Bo: Lunch", "Re: Lunch", "dinner on friday", "Pick hotel",
		`aria-labelledby="waiting-h"`, "Quote", `href="/chat/1"`, `href="/drafts/1"`,
		`hx-post="/loops/commitments/1/done"`, `hx-post="/loops/1/dismiss"`, `hx-post="/proposals/10/approve"`, `hx-post="/drafts/1/discard"`,
		`action="/attention/snooze"`, `aria-label="Counts"`, "Proposal <span", "Commitment <span", "Draft <span",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if draft := attentionRow(page, "Re: Lunch"); strings.Contains(draft, "/send") || !strings.Contains(draft, `href="/drafts/1"`) {
		t.Errorf("draft row: %s", draft)
	}
	if split := attentionRow(page, "Split receipts"); strings.Contains(split, "/approve") || !strings.Contains(split, "/proposals/1/dismiss") {
		t.Errorf("a proposal that needs a message selection offers inline approve: %s", split)
	}
	if event := attentionRow(page, "dinner on friday"); strings.Contains(event, "<a ") || strings.Contains(event, "hx-post") {
		t.Errorf("event candidate row has a link or action: %s", event)
	}
	if waiting := attentionRow(page, "Quote"); strings.Contains(waiting, "<form") || strings.Contains(waiting, "<a ") {
		t.Errorf("waiting row has actions: %s", waiting)
	}
	if !strings.Contains(page, "<html") {
		t.Errorf("a plain request should get the full page")
	}
	_, fragment, _ := s.do("GET", "/attention", nil, true)
	if strings.Contains(fragment, "<html") || !strings.Contains(fragment, `id="attention"`) {
		t.Errorf("an htmx request should get the fragment: %.200s", fragment)
	}
}

func TestAttentionPageOmitsADisabledModulesItems(t *testing.T) {
	s := startAttentionSite(t, flagsExcept(module.Mail, "false"))
	page := s.get("/attention")
	for _, gone := range []string{"Send the report", "Bo: Lunch", "Quote", "Draft <span", "Commitment <span"} {
		if strings.Contains(page, gone) {
			t.Errorf("page still shows %q with mail disabled", gone)
		}
	}
	for _, kept := range []string{"Split receipts", "Pick hotel"} {
		if !strings.Contains(page, kept) {
			t.Errorf("page lost %q", kept)
		}
	}
}

func TestAttentionPageApprovesAProposalLikeTheProposalsPage(t *testing.T) {
	s := startAttentionSite(t, flagsExcept(module.Mail, "true"))
	form := attentionRow(s.get("/attention"), "Remind me 10")
	match := regexp.MustCompile(`hx-post="(/proposals/10/approve)"`).FindStringSubmatch(form)
	if match == nil {
		t.Fatalf("no approve form for the reminder: %s", form)
	}
	if status, body, _ := s.post(match[1], url.Values{}); status != http.StatusOK || !strings.Contains(body, "Applied") {
		t.Fatalf("approve from the attention page = %d: %.300s", status, body)
	}
	if status, body, _ := s.post("/proposals/11/approve", url.Values{}); status != http.StatusOK || !strings.Contains(body, "Applied") {
		t.Fatalf("approve from the proposals page = %d: %.300s", status, body)
	}
	for _, id := range []string{"10", "11"} {
		if got := strings.Join(s.sql("SELECT status FROM proposals WHERE id = ?", id), ""); got != "applied" {
			t.Errorf("proposal %s is %q", id, got)
		}
	}
	summaries := s.sql("SELECT json_extract(result, '$.summary') FROM proposals WHERE id IN (10, 11)")
	if len(summaries) != 2 || summaries[0] != summaries[1] || !strings.HasPrefix(summaries[0], "Reminder scheduled for ") {
		t.Errorf("result summaries differ: %v", summaries)
	}
	actions := s.sql("SELECT json_extract(action, '$.type'), json_extract(undo, '$.op'), ok FROM proposal_actions ORDER BY proposal_id")
	if len(actions) != 2 || actions[0] != actions[1] || actions[0] != "reminder|reminder_cancel|1" {
		t.Errorf("proposal_actions differ: %v", actions)
	}
	if jobs := s.sql("SELECT COUNT(*) FROM jobs WHERE name LIKE 'Reminder: %' AND cancelled = 0"); jobs[0] != "2" {
		t.Errorf("reminder jobs %v, want 2", jobs)
	}
	if after := s.get("/attention"); strings.Contains(after, "Remind me 10") || strings.Contains(after, "Remind me 11") {
		t.Errorf("applied proposals still need attention")
	}
	if applied := s.get("/proposals?status=applied"); strings.Count(applied, `hx-post="/proposals/10/undo"`) != 1 || strings.Count(applied, `hx-post="/proposals/11/undo"`) != 1 {
		t.Errorf("both applied proposals should be undoable: %.500s", applied)
	}
	if status, body, _ := s.post("/proposals/10/undo", url.Values{}); status != http.StatusOK || !strings.Contains(body, "Undone") {
		t.Errorf("undo of the proposal approved from the attention page = %d: %.300s", status, body)
	}
	if jobs := s.sql("SELECT COUNT(*) FROM jobs WHERE name LIKE 'Reminder: %' AND cancelled = 0"); jobs[0] != "1" {
		t.Errorf("undo should cancel one reminder job, %v remain", jobs)
	}
}

func TestAttentionPageActionsPersistAsOnTheirSourcePages(t *testing.T) {
	s := startAttentionSite(t, flagsExcept(module.Mail, "true"))
	s.get("/attention")
	for _, path := range []string{"/loops/commitments/1/done", "/loops/1/dismiss", "/drafts/1/discard", "/proposals/1/dismiss"} {
		if status, body, _ := s.post(path, url.Values{}); status != http.StatusOK {
			t.Fatalf("POST %s = %d: %.300s", path, status, body)
		}
	}
	for query, want := range map[string]string{
		"SELECT status FROM commitments WHERE id = 1":              "done",
		"SELECT dismissed FROM open_loops WHERE thread_key = 't1'": "1",
		"SELECT status FROM drafts WHERE id = 1":                   "discarded",
		"SELECT status FROM proposals WHERE id = 1":                "dismissed",
	} {
		if got := strings.Join(s.sql(query), ""); got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
	page := s.get("/attention")
	for _, gone := range []string{"Send the report", "Bo: Lunch", "Split receipts"} {
		if strings.Contains(page, gone) {
			t.Errorf("%q still listed after its action", gone)
		}
	}
}

func TestAttentionSnoozePostHidesTheItemUntilItsDate(t *testing.T) {
	s := startAttentionSite(t, flagsExcept(module.Mail, "true"))
	s.get("/attention")
	until := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	status, _, header := s.do("POST", "/attention/snooze", url.Values{"kind": {"owe_reply"}, "source_id": {"1"}, "until": {until}}, false)
	if status != http.StatusSeeOther || header.Get("Location") != "/attention" {
		t.Fatalf("snooze = %d %q", status, header.Get("Location"))
	}
	if strings.Contains(s.get("/attention"), "Bo: Lunch") {
		t.Errorf("snoozed item still listed")
	}
	if got := strings.Join(s.sql("SELECT kind, source_id FROM attention_snoozes"), ","); got != "owe_reply|1" {
		t.Errorf("attention_snoozes %q", got)
	}
	s.exec("UPDATE attention_snoozes SET until = ?", float64(time.Now().Add(-time.Minute).Unix()))
	if !strings.Contains(s.get("/attention"), "Bo: Lunch") {
		t.Errorf("item did not reappear after the snooze date")
	}
	for _, form := range []url.Values{
		{"kind": {"nonsense"}, "source_id": {"1"}, "until": {until}},
		{"kind": {"owe_reply"}, "source_id": {""}, "until": {until}},
		{"kind": {"owe_reply"}, "source_id": {"1"}, "until": {"soon"}},
	} {
		if status, _, _ := s.do("POST", "/attention/snooze", form, false); status != http.StatusUnprocessableEntity {
			t.Errorf("snooze %v = %d, want 422", form, status)
		}
	}
	jar, _ := cookiejar.New(nil)
	if status, _, _ := fetch(t, &http.Client{Jar: jar}, "POST", s.base+"/attention/snooze", url.Values{"kind": {"owe_reply"}, "source_id": {"1"}, "until": {until}}); status != http.StatusForbidden {
		t.Errorf("snooze without a session token = %d, want 403", status)
	}
}
