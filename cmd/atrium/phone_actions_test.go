package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

const phoneKind = "media_channel_add"

type phoneSite struct {
	t      *testing.T
	dir    string
	base   string
	client *http.Client
	api    *modelLog
	runs   int
	stop   func()
}

func (s *phoneSite) restart() {
	s.t.Helper()
	s.base, _, s.stop = startServeStoppable(s.t, s.dir)
}

func newPhoneSite(t *testing.T, dir string, api *modelLog) *phoneSite {
	t.Helper()
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	jar, _ := cookiejar.New(nil)
	return &phoneSite{t: t, dir: dir, base: base, client: &http.Client{Jar: jar}, api: api, stop: stop}
}

func (s *phoneSite) page() string {
	s.t.Helper()
	code, body, _ := fetch(s.t, s.client, http.MethodGet, s.base+"/settings/notifications", nil)
	if code != http.StatusOK {
		s.t.Fatalf("notifications page = %d", code)
	}
	return body
}

func (s *phoneSite) post(path string, form url.Values) (int, string) {
	s.t.Helper()
	s.page()
	if form == nil {
		form = url.Values{}
	}
	code, body, _ := fetch(s.t, s.client, http.MethodPost, s.base+path, form)
	return code, body
}

func (s *phoneSite) policy(kinds ...string) {
	s.t.Helper()
	if code, _ := s.post("/settings/phone-actions", url.Values{"enabled": {"1"}, "kind": kinds}); code != http.StatusOK {
		s.t.Fatalf("save phone actions = %d", code)
	}
}

func (s *phoneSite) propose(n int) {
	s.t.Helper()
	blocks := make([]json.RawMessage, 0, n)
	for range n {
		s.runs++
		blocks = append(blocks, anthropictest.ToolUse(fmt.Sprintf("w%d", s.runs), phoneKind, map[string]any{
			"imdb_id": fmt.Sprintf("tt%07d", s.runs), "regex": "Show", "name": fmt.Sprintf("Show %d", s.runs),
		}))
	}
	s.api.Push(anthropictest.Reply("tool_use", blocks...), anthropictest.Reply("end_turn", anthropictest.Text("proposed")))
	if ran := runApp(s.t, "agent", "run", "--kind", "chat", "--text", "watch these shows"); ran.code != cli.ExitOK {
		s.t.Fatalf("propose run: %d %s", ran.code, ran.stderr)
	}
}

func (s *phoneSite) action(token string) int {
	s.t.Helper()
	code, _, _ := fetch(s.t, s.client, http.MethodPost, s.base+"/actions/"+token, nil)
	return code
}

func TestPhoneActionsControlsLiveOnTheNotificationsPage(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	s := newPhoneSite(t, dir, api)
	db := filepath.Join(dir, "atrium.db")
	if body := s.page(); !strings.Contains(body, `data-phone-outstanding>0 `) {
		t.Fatal("notifications page misses the zero outstanding count")
	}

	noFollow := &http.Client{Jar: s.client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	code, _, header := fetch(t, noFollow, http.MethodPost, s.base+"/settings/phone-actions", url.Values{"enabled": {"1"}, "kind": {"reminder", phoneKind, "gmail_label_merge"}})
	if code != http.StatusSeeOther || header.Get("Location") != "/settings/notifications#phone-actions" {
		t.Fatalf("save = %d to %q", code, header.Get("Location"))
	}
	if v, _ := storedSetting(t, db, policy.EnabledKey); v != "true" {
		t.Fatalf("stored %s = %q", policy.EnabledKey, v)
	}
	if v, _ := storedSetting(t, db, policy.KindsKey); v != "reminder,"+phoneKind {
		t.Fatalf("stored %s = %q, want the web-only kind dropped", policy.KindsKey, v)
	}

	s.propose(1)
	body := s.page()
	for _, want := range []string{`id="phone-actions"`, `data-phone-outstanding>1 `, `action="/settings/phone-actions"`, `action="/settings/phone-actions/revoke"`,
		`name="enabled" value="1" checked`, `name="kind" value="reminder" checked`, `name="kind" value="gmail_label_merge" disabled`} {
		if !strings.Contains(body, want) {
			t.Fatalf("notifications page misses %q", want)
		}
	}
	if strings.Contains(body, `name="kind" value="memory" checked`) {
		t.Fatal("notifications page shows an unsaved kind as enabled")
	}
	if strings.Contains(body, `href="/settings/phone-actions"`) {
		t.Error("the settings tabs still link the phone actions page")
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/phone-actions", nil); code == http.StatusOK {
		t.Errorf("GET /settings/phone-actions = %d, want it gone", code)
	}

	code, body = s.post("/settings/phone-actions/revoke", nil)
	if code != http.StatusOK || !strings.Contains(body, `id="phone-actions"`) || !strings.Contains(body, `data-phone-outstanding>0 `) {
		t.Fatalf("revoke = %d, want the notifications page back with no outstanding links", code)
	}
	d := mustConnect(t, db, keyFor(t))
	defer func() { _ = d.Close() }()
	var outstanding int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM action_links WHERE used_ts IS NULL").Scan(&outstanding); err != nil || outstanding != 0 {
		t.Fatalf("outstanding links = %d, %v", outstanding, err)
	}

}

func TestPhoneNotificationActionLinksRefuseReplayAlterationRotationDisallowedKindsAndRateLimit(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	s := newPhoneSite(t, dir, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	if code, _ := s.post("/settings/notifications/push/subscriptions", browser.form()); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	s.policy("reminder", phoneKind)

	pushes := 0
	links := func() (string, string) {
		t.Helper()
		pushes++
		var n struct {
			Body    string `json:"body"`
			Actions []struct {
				Action string `json:"action"`
				Title  string `json:"title"`
				URL    string `json:"url"`
			} `json:"actions"`
		}
		raw, err := json.Marshal(browser.decrypt(t, push.wait(t, pushes).body))
		if err != nil || json.Unmarshal(raw, &n) != nil {
			t.Fatalf("payload: %v", err)
		}
		if strings.Contains(n.Body, "Show ") || !strings.Contains(n.Body, "Proposal #") {
			t.Fatalf("notification body = %q", n.Body)
		}
		if len(n.Actions) != 2 || n.Actions[0].Title != "Approve" || n.Actions[1].Title != "Dismiss" {
			t.Fatalf("actions = %#v", n.Actions)
		}
		for _, a := range n.Actions {
			if !strings.HasPrefix(a.URL, "/actions/") || a.Action == "" {
				t.Fatalf("action = %#v", a)
			}
		}
		return strings.TrimPrefix(n.Actions[0].URL, "/actions/"), strings.TrimPrefix(n.Actions[1].URL, "/actions/")
	}
	status := func(title string) string {
		t.Helper()
		var st string
		if err := d.Read.QueryRow("SELECT status FROM proposals WHERE title = ?", "Watch show: "+title).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	s.propose(1)
	_, dismiss := links()
	if code := s.action(dismiss); code != http.StatusOK {
		t.Fatalf("dismiss = %d", code)
	}
	if st := status("Show 1"); st != "dismissed" {
		t.Fatalf("status = %s", st)
	}

	s.propose(1)
	approve, sibling := links()
	if code := s.action(approve); code != http.StatusOK {
		t.Fatalf("first approve = %d", code)
	}
	if st := status("Show 2"); st != "applied" {
		t.Fatalf("approved status = %s", st)
	}
	if code := s.action(approve); code != http.StatusGone {
		t.Fatalf("replay = %d, want 410", code)
	}
	if code := s.action(sibling); code != http.StatusGone {
		t.Fatalf("dismiss after approve = %d, want 410", code)
	}
	if code := s.action("A" + approve[1:]); code != http.StatusBadRequest {
		t.Fatalf("altered = %d, want 400", code)
	}

	s.propose(1)
	changed, _ := links()
	if _, err := d.Write.Exec("UPDATE proposals SET fingerprint = 'changed' WHERE title = 'Watch show: Show 3'"); err != nil {
		t.Fatal(err)
	}
	if code := s.action(changed); code != http.StatusBadRequest {
		t.Fatalf("changed plan = %d", code)
	}
	var changedAudit int
	if err := d.Read.QueryRow("SELECT count(*) FROM operation_audit WHERE source = 'phone' AND operation = 'review.approve' AND outcome = 'invalid'").Scan(&changedAudit); err != nil || changedAudit != 1 {
		t.Fatalf("changed plan audit = %d, %v", changedAudit, err)
	}
	rotated := changed
	if code, _ := s.post("/settings/phone-actions/revoke", nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if code := s.action(rotated); code != http.StatusBadRequest {
		t.Fatalf("rotated = %d, want 400", code)
	}
	if st := status("Show 3"); st != "pending" {
		t.Fatalf("rotated link changed the proposal to %s", st)
	}

	s.propose(1)
	_, denied := links()
	if _, err := d.Write.Exec("ALTER TABLE settings RENAME TO unavailable_settings"); err != nil {
		t.Fatal(err)
	}
	code, body, _ := fetch(t, s.client, http.MethodPost, s.base+"/actions/"+denied, nil)
	if _, err := d.Write.Exec("ALTER TABLE unavailable_settings RENAME TO settings"); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusInternalServerError || strings.TrimSpace(body) != "action link unavailable" {
		t.Fatalf("policy failure = %d %q", code, body)
	}
	var failedAudit int
	if err := d.Read.QueryRow("SELECT count(*) FROM operation_audit WHERE source = 'phone' AND operation = 'review.dismiss' AND outcome = 'failed'").Scan(&failedAudit); err != nil || failedAudit != 1 {
		t.Fatalf("policy failure audit = %d, %v", failedAudit, err)
	}
	s.policy("reminder")
	if code := s.action(denied); code != http.StatusForbidden {
		t.Fatalf("disallowed kind = %d, want 403", code)
	}
	if st := status("Show 4"); st != "pending" {
		t.Fatalf("refused link changed the proposal to %s", st)
	}

	var auditName, outcome, target string
	if err := d.Read.QueryRow("SELECT operation, outcome, target FROM operation_audit WHERE source = 'phone' ORDER BY id DESC LIMIT 1").Scan(&auditName, &outcome, &target); err != nil || auditName != "review.dismiss" || outcome != "refused" || !strings.HasPrefix(target, "proposal:") {
		t.Fatalf("phone refusal audit = %q %q %q, %v", auditName, outcome, target, err)
	}
	if code := s.action(denied); code != http.StatusGone {
		t.Fatalf("refused token replay = %d", code)
	}
	rows, err := d.Read.Query("SELECT operation || ' ' || outcome || ' ' || target FROM operation_audit WHERE source = 'phone' AND actor = 'phone' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var phoneAudit []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		phoneAudit = append(phoneAudit, row)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	wantAudit := []string{"review.dismiss ok proposal:1", "media.channel-add ok channel:1", "review.approve ok proposal:2", "review.approve invalid proposal:3", "review.dismiss failed proposal:4", "review.dismiss refused proposal:4"}
	if !slices.Equal(phoneAudit, wantAudit) {
		t.Fatalf("phone audit = %q, want %q", phoneAudit, wantAudit)
	}
	var count int
	if err := d.Read.QueryRow("SELECT count(*) FROM operation_audit WHERE source = 'phone'").Scan(&count); err != nil || count != len(wantAudit) {
		t.Fatalf("phone audit count = %d, %v", count, err)
	}

	s.policy("reminder", phoneKind)
	spent := 4
	s.propose(actionlink.RateLimit)
	var proposed, notified int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = ?", phoneKind).Scan(&proposed); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications WHERE title = ?", "atrium: "+phoneKind+" proposal").Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if proposed != 4+actionlink.RateLimit || notified != 4+actionlink.RateLimit-spent {
		t.Fatalf("%d proposals and %d phone notifications, want %d and %d", proposed, notified, 4+actionlink.RateLimit, 4+actionlink.RateLimit-spent)
	}
	var outstanding int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM action_links WHERE used_ts IS NULL").Scan(&outstanding); err != nil || outstanding != actionlink.RateLimit-spent {
		t.Fatalf("outstanding links = %d, %v", outstanding, err)
	}
	if body := s.page(); !strings.Contains(body, fmt.Sprintf("data-phone-outstanding>%d ", actionlink.RateLimit-spent)) {
		t.Fatalf("notifications page misses %d outstanding links", actionlink.RateLimit-spent)
	}

}
