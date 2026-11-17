package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
)

type statusClock struct {
	mu sync.Mutex
	at float64
}

func (c *statusClock) now() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *statusClock) advance(seconds float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at += seconds
}

type statusSite struct {
	m     *mailSafety
	s     *apiSite
	token string
	clock *statusClock
}

func statusFixture(t *testing.T, mode string, fake *mailtest.FakeClient) *statusSite {
	t.Helper()
	clock := &statusClock{at: float64(time.Now().UnixNano()) / 1e9}
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	f.app = newAppWith(f.lookup, func(config.Config) mail.OpenFunc { return fake.Open }, clock.now, quietLog)
	f.settings, f.state = f.app.Settings, f.app.State
	seedSettings(t, map[string]string{mail.KeyMode: mode})
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, "http://127.0.0.1:1/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"work", "icloud", "atrium.test@example.test", "app-pass", false}))
	m := &mailSafety{dir: dir, app: f.app, fake: fake, path: filepath.Join(dir, "atrium.db")}
	m.run(t, "sync")
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, m.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return &statusSite{m: m, s: &apiSite{t: t, base: base}, token: token, clock: clock}
}

func seenInbox(subjects ...string) *mailtest.FakeClient {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Archive", mailtest.NewBox(2))
	for _, subject := range subjects {
		h, x := mailtest.RawWith(subject, "friend@example.test", strings.ReplaceAll(subject, " ", "-"), "Hello.")
		inbox.Add(h, x, []string{`\Seen`}, nil)
	}
	return fake
}

func (s *statusSite) post(path, token string, body any, headers ...string) apiReply {
	return s.s.do(http.MethodPost, "/api/v1"+path, token, body, headers...)
}

func (s *statusSite) status(t *testing.T) map[string]any {
	t.Helper()
	got := s.s.do(http.MethodGet, "/api/v1/mail/status", s.token, nil)
	if got.status != http.StatusOK {
		t.Fatalf("status read: %d %.400s", got.status, got.body)
	}
	data, _ := got.json["data"].(map[string]any)
	if data == nil {
		t.Fatalf("status read has no data: %.400s", got.body)
	}
	return data
}

func (s *statusSite) mode(t *testing.T) string {
	t.Helper()
	return s.m.scalar(t, "SELECT value FROM settings WHERE key = ?", mail.KeyMode)
}

func (s *statusSite) confirm(t *testing.T, action string) map[string]any {
	t.Helper()
	got := s.post("/mail/status/confirm", s.token, map[string]any{"action": action})
	if got.status != http.StatusOK {
		t.Fatalf("confirm %s: %d %.400s", action, got.status, got.body)
	}
	data, _ := got.json["data"].(map[string]any)
	if token, _ := data["confirm_token"].(string); token == "" {
		t.Fatalf("confirm %s returned no token: %.400s", action, got.body)
	}
	return data
}

func issueAgentToken(t *testing.T) string {
	t.Helper()
	got := runApp(t, "auth", "token", "issue", "--name", "browser-agent", "--agent")
	if got.code != cli.ExitOK {
		t.Fatalf("issue agent token: exit %d stderr %q", got.code, got.stderr)
	}
	return strings.TrimSpace(got.stdout)
}

func TestMailStatusGoLiveNeedsItsConfirmTokenAndOpensTheWatchWindow(t *testing.T) {
	s := statusFixture(t, "shadow", seenInbox("Old note one", "Old note two"))
	before := s.status(t)
	if before["mode"] != "shadow" || before["eligible"] != true || len(before["unmet"].([]any)) != 0 {
		t.Fatalf("shadow status: %v", before)
	}
	if accounts := before["accounts"].([]any); len(accounts) != 1 || accounts[0] != "work" {
		t.Fatalf("accounts: %v", before["accounts"])
	}

	expectAPI(t, s.post("/mail/status/go-live", s.token, map[string]any{"confirm_token": ""}), http.StatusConflict, "confirm_required")
	expectAPI(t, s.post("/mail/status/go-live", s.token, map[string]any{"confirm_token": "not-a-token"}), http.StatusConflict, "confirm_required")
	if s.mode(t) != "shadow" {
		t.Fatalf("mode changed without a token: %s", s.mode(t))
	}
	client := mrvClient(t)
	if code, _, _ := mrvDo(t, client, http.MethodGet, s.s.base+"/live", nil); code != http.StatusOK {
		t.Fatalf("status page: %d", code)
	}
	code, body, _ := mrvDo(t, client, http.MethodPost, s.s.base+"/live/confirm", url.Values{"action": {"go_live"}}, "Origin", s.s.base)
	if code != http.StatusOK || !strings.Contains(body, `data-testid="confirm-statement"`) || !strings.Contains(body, `href="/live" autofocus>Cancel`) || !strings.Contains(body, `href="#pause-control"`) {
		t.Fatalf("page confirm: %d %.600s", code, body)
	}
	if code, _, _ := mrvDo(t, client, http.MethodPost, s.s.base+"/live/go-live", url.Values{}, "Origin", s.s.base); code != http.StatusConflict || s.mode(t) != "shadow" {
		t.Fatalf("page go-live without a token: %d, mode %s", code, s.mode(t))
	}

	effect := s.confirm(t, "go_live")
	statement, _ := effect["statement"].(string)
	if !strings.Contains(statement, "work") || !strings.Contains(statement, "live") || effect["pause"] == "" {
		t.Fatalf("effect statement: %v", effect)
	}
	if accounts := effect["accounts"].([]any); len(accounts) != 1 || accounts[0] != "work" {
		t.Fatalf("effect accounts: %v", effect["accounts"])
	}
	if effect["write_cap"].(float64) <= 0 || effect["baseline"].(float64) != 2 {
		t.Fatalf("effect counts: %v", effect)
	}
	token := effect["confirm_token"].(string)

	if got := s.post("/mail/status/confirm", s.token, map[string]any{"action": "go_live"}, "Atrium-Agent", "1"); got.status != http.StatusForbidden || got.code() != "agent_caller" {
		t.Fatalf("agent-marked confirm: %d %.300s", got.status, got.body)
	}
	expectAPI(t, s.post("/mail/status/go-live", s.token, map[string]any{"confirm_token": token}, "Atrium-Agent", "1"), http.StatusForbidden, "agent_caller")
	agent := issueAgentToken(t)
	expectAPI(t, s.post("/mail/status/go-live", agent, map[string]any{"confirm_token": token}), http.StatusForbidden, "agent_caller")
	if s.mode(t) != "shadow" {
		t.Fatalf("an agent caller changed the mode: %s", s.mode(t))
	}

	switched := s.post("/mail/status/go-live", s.token, map[string]any{"confirm_token": token})
	if switched.status != http.StatusOK {
		t.Fatalf("go-live: %d %.400s", switched.status, switched.body)
	}
	if s.mode(t) != "live" {
		t.Fatalf("mode after go-live: %s", s.mode(t))
	}
	if n := s.m.scalar(t, "SELECT COUNT(*) FROM settings_audit WHERE key = ? AND new_value = 'live' AND source = 'api'", mail.KeyMode); n != "1" {
		t.Fatalf("settings audit rows for the switch: %s", n)
	}
	if n := s.m.scalar(t, "SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.go-live' AND outcome = 'ok'"); n != "1" {
		t.Fatalf("go-live audit rows: %s", n)
	}
	expectAPI(t, s.post("/mail/status/go-live", s.token, map[string]any{"confirm_token": token}), http.StatusConflict, "not_eligible")

	after := s.status(t)
	watch := after["watch_closely"].(map[string]any)
	if after["mode"] != "live" || watch["active"] != true || watch["mode"] != "live" {
		t.Fatalf("status after go-live: %v", after)
	}
	if unmet := after["unmet"].([]any); len(unmet) != 1 || unmet[0] != "already_live" {
		t.Fatalf("unmet after go-live: %v", after["unmet"])
	}
	if until, since := watch["until"].(float64), watch["since"].(float64); until-since != 7*86400 {
		t.Fatalf("watch window: %v", watch)
	}

	if code, body, _ := mrvDo(t, client, http.MethodGet, s.s.base+"/live", nil); code != http.StatusOK || !strings.Contains(body, `data-testid="watch-closely"`) || !strings.Contains(body, `data-testid="mail-mode">live<`) {
		t.Fatalf("status page after go-live: %d %.600s", code, body)
	}
	s.clock.advance(6 * 86400)
	if watch := s.status(t)["watch_closely"].(map[string]any); watch["active"] != true {
		t.Fatalf("watch window closed after 6 days: %v", watch)
	}
	s.clock.advance(2 * 86400)
	if watch := s.status(t)["watch_closely"].(map[string]any); watch["active"] != false {
		t.Fatalf("watch window still open after 8 days: %v", watch)
	}
}

func TestMailStatusGoLiveIsRefusedWhileAWriteIsUnresolved(t *testing.T) {
	s := statusFixture(t, "shadow", seenInbox("Note"))
	token := s.confirm(t, "go_live")["confirm_token"].(string)
	s.m.exec(t, "INSERT INTO effects (created_at, lease_at, settled_at, token, origin, operation, operation_key, direction, target, state, account) VALUES (1, 1, 1, 'status-test', ?, 'archive', 'k', 'forward', 'INBOX/1', 'unknown', 'work')", "operator_mail")
	data := s.status(t)
	if data["eligible"] != false || len(data["unmet"].([]any)) != 1 || data["unmet"].([]any)[0] != "unresolved_writes" {
		t.Fatalf("status with an unknown write: %v", data)
	}
	expectAPI(t, s.post("/mail/status/confirm", s.token, map[string]any{"action": "go_live"}), http.StatusConflict, "not_eligible")
	expectAPI(t, s.post("/mail/status/go-live", s.token, map[string]any{"confirm_token": token}), http.StatusConflict, "not_eligible")
	if s.mode(t) != "shadow" {
		t.Fatalf("mode changed while not eligible: %s", s.mode(t))
	}
}

func TestMailStatusRebaselineReplayAndDeletions(t *testing.T) {
	s := statusFixture(t, "live", seenInbox("First", "Second", "Third"))
	expectAPI(t, s.post("/mail/status/rebaseline", s.token, map[string]any{"confirm_token": ""}), http.StatusConflict, "confirm_required")
	golive := s.m.scalar(t, "SELECT value FROM live_state WHERE key = 'golive_ts'")
	if s.m.scalar(t, "SELECT value FROM live_state WHERE key = 'golive_ts'") != golive {
		t.Fatal("baseline retaken without a token")
	}
	effect := s.confirm(t, "rebaseline")
	if !strings.Contains(effect["statement"].(string), "baseline") || effect["baseline"].(float64) != 3 {
		t.Fatalf("rebaseline effect: %v", effect)
	}
	expectAPI(t, s.post("/mail/status/rebaseline", issueAgentToken(t), map[string]any{"confirm_token": effect["confirm_token"]}), http.StatusForbidden, "agent_caller")
	if got := s.post("/mail/status/rebaseline", s.token, map[string]any{"confirm_token": effect["confirm_token"]}); got.status != http.StatusOK {
		t.Fatalf("rebaseline: %d %.400s", got.status, got.body)
	}
	expectAPI(t, s.post("/mail/status/rebaseline", s.token, map[string]any{"confirm_token": effect["confirm_token"]}), http.StatusConflict, "confirm_required")
	if s.m.scalar(t, "SELECT value FROM live_state WHERE key = 'golive_ts'") == golive {
		t.Fatal("the confirmed rebaseline did not retake the baseline")
	}
	baseline := s.status(t)["baseline"].(map[string]any)
	if baseline["messages"].(float64) != 3 || baseline["taken_at"] == nil {
		t.Fatalf("baseline after rebaseline: %v", baseline)
	}

	if s.status(t)["replay"] != nil {
		t.Fatal("a replay summary exists before any run")
	}
	expectAPI(t, s.post("/mail/status/replay", s.token, nil, "Atrium-Agent", "1"), http.StatusForbidden, "agent_caller")
	ran := s.post("/mail/status/replay", s.token, nil)
	if ran.status != http.StatusOK {
		t.Fatalf("replay run: %d %.400s", ran.status, ran.body)
	}
	if strings.Contains(ran.body, `"lines"`) || strings.Contains(ran.body, "First") {
		t.Fatalf("the replay run returned its lines: %.400s", ran.body)
	}
	out := ran.json["data"].(map[string]any)
	runID := out["run_id"].(float64)
	if runID <= 0 {
		t.Fatalf("replay run id: %v", out)
	}
	if got := s.m.scalar(t, "SELECT r.status FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE r.id = ? AND j.module = 'mail' AND j.name = 'replay'", int64(runID)); got != "ok" {
		t.Fatalf("job run %v status: %s", runID, got)
	}
	summary := s.status(t)["replay"].(map[string]any)
	if summary["run_id"] != runID || summary["messages"] != out["messages"] || summary["differ"] != out["differ"] {
		t.Fatalf("replay summary %v, run %v", summary, out)
	}

	s.m.exec(t, "INSERT INTO proposals (kind, account, status, created_ts, title, fingerprint) VALUES ('deletion_rule', 'work', 'pending', 1, 'Delete old receipts', 'status-test')")
	if got := s.status(t)["deletions"].(map[string]any)["pending_proposals"]; got != float64(1) {
		t.Fatalf("pending deletion proposals: %v", got)
	}
}

func TestMailModeNeverChangesThroughAnAgentPath(t *testing.T) {
	s := statusFixture(t, "shadow", seenInbox("Note"))
	agent := issueAgentToken(t)
	for _, path := range []string{"/settings/" + mail.KeyMode} {
		got := s.s.do(http.MethodPut, "/api/v1"+path, agent, map[string]any{"value": "live"})
		if got.status == http.StatusOK {
			t.Fatalf("agent bearer set the mode: %.300s", got.body)
		}
		got = s.s.do(http.MethodDelete, "/api/v1"+path, agent, nil)
		if got.status == http.StatusOK {
			t.Fatalf("agent bearer unset the mode: %.300s", got.body)
		}
	}
	patch := s.s.do(http.MethodPatch, "/api/v1/settings", agent, map[string]any{"set": map[string]string{mail.KeyMode: "live"}})
	if patch.status == http.StatusOK {
		t.Fatalf("agent bearer patched the mode: %.300s", patch.body)
	}
	for _, header := range []string{"", "Atrium-Agent"} {
		token := agent
		var headers []string
		if header != "" {
			token, headers = s.token, []string{header, "1"}
		}
		expectAPI(t, s.post("/mail/status/confirm", token, map[string]any{"action": "go_live"}, headers...), http.StatusForbidden, "agent_caller")
		expectAPI(t, s.post("/mail/status/go-live", token, map[string]any{"confirm_token": "x"}, headers...), http.StatusForbidden, "agent_caller")
	}
	if s.mode(t) != "shadow" {
		t.Fatalf("mode after agent bearer calls: %s", s.mode(t))
	}

	report, code := runCatalogOperations(t, "--json")
	if code != 0 {
		t.Fatalf("catalog operations exit %d", code)
	}
	for _, name := range []string{"mail.status-confirm", "mail.go-live", "mail.status-rebaseline", "mail.replay-run", "mail.rebaseline", "settings.set", "settings.unset"} {
		row, ok := hasOperation(report.Operations, name)
		if !ok || !slices.Equal(row.Authority, []string{"operator"}) {
			t.Fatalf("operation %s: %v %v", name, ok, row)
		}
		for _, b := range row.Bindings {
			if b.Kind == "tool" {
				t.Fatalf("operation %s has a tool binding %s", name, b.Target)
			}
		}
	}
	for _, kind := range []string{"chat", "cron"} {
		if out := toolResultKind(t, kind, "mail_go_live", map[string]any{"confirm_token": "x"}); !strings.Contains(strings.ToLower(out), "unknown tool") {
			t.Fatalf("%s run tool mail_go_live: %q", kind, out)
		}
	}
	if s.mode(t) != "shadow" {
		t.Fatalf("mode after tool calls: %s", s.mode(t))
	}

	if got := s.post("/mail/status/replay", s.token, nil); got.status != http.StatusOK {
		t.Fatalf("replay run: %d %.300s", got.status, got.body)
	}
	if s.mode(t) != "shadow" || s.m.scalar(t, "SELECT COUNT(*) FROM settings_audit WHERE key = ?", mail.KeyMode) != s.m.scalar(t, "SELECT COUNT(*) FROM settings_audit WHERE key = ? AND source != 'tool'", mail.KeyMode) {
		t.Fatalf("mode after the job worker ran: %s", s.mode(t))
	}
}
