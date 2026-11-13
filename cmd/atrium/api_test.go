package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

const apiClient = "atrium-ios"

type apiSite struct {
	t    *testing.T
	base string
	dir  string
	idp  *oidctest.Provider
	api  *modelLog
}

type apiReply struct {
	status int
	header http.Header
	body   string
	json   map[string]any
}

func (r apiReply) code() string {
	e, _ := r.json["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func newAPISite(t *testing.T) *apiSite {
	t.Helper()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	return startAPISite(t, dir, api)
}

func startAPISite(t *testing.T, dir string, api *modelLog, extra ...string) *apiSite {
	t.Helper()
	idp := oidctest.New(t, oidctest.Options{ClientSecret: "s3cret-for-the-test", GroupsIn: "userinfo"})
	idp.AddUser(oidctest.User{Name: "owner", Email: "owner@example.com", Groups: []string{"atrium_access", "atrium_admin"}})
	idp.AddUser(oidctest.User{Name: "guest", Email: "guest@example.com", Groups: []string{"atrium_access"}})
	idp.AddUser(oidctest.User{Name: "stranger", Email: "stranger@example.com"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	base := "http://127.0.0.1:" + port
	setEnv(t, settingEnv(config.KeyPublicURL), base)
	setEnv(t, "ATRIUM_HOST_CONFIG", oidcHostConfig(t, dir, oidcHost{
		Issuer:      idp.Issuer(),
		ClientID:    idp.ClientID(),
		APIClientID: apiClient,
		Secret:      "s3cret-for-the-test",
		GroupsClaim: "groups",
	}))
	args := []string{"--port", port, "--auth", "proxy", "--auth", "oidc", "--allowed-host", "127.0.0.1"}
	args = append(args, extra...)
	served, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, args...)
	if err != nil {
		t.Fatal(err)
	}
	if served != base {
		t.Fatalf("serve listens on %s, want %s", served, base)
	}
	t.Cleanup(stop)
	return &apiSite{t: t, base: base, dir: dir, idp: idp, api: api}
}

type oidcHost struct {
	Issuer       string
	ClientID     string
	APIClientID  string
	Secret       string
	GroupsClaim  string
	AccessGroups string
	Scopes       string
	Preset       string
}

func oidcHostConfig(t *testing.T, dir string, h oidcHost) string {
	t.Helper()
	if h.Preset == "" {
		h.Preset = oidc.PresetGeneric
	}
	if h.GroupsClaim == "" {
		h.GroupsClaim = "groups"
	}
	if h.Scopes == "" {
		h.Scopes = "openid profile email groups"
	}
	base := defaultHostConfig
	if f := fixtureFor(t); f != nil {
		if path, ok := f.lookup("ATRIUM_HOST_CONFIG"); ok && path != "" {
			base = path
		}
	}
	existing, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	config := [][2]string{
		{"preset", h.Preset},
		{"issuer", h.Issuer},
		{"client_id", h.ClientID},
		{"scopes", h.Scopes},
		{"groups_claim", h.GroupsClaim},
	}
	if h.APIClientID != "" {
		config = append(config, [2]string{"api_client_id", h.APIClientID})
	}
	if h.AccessGroups != "" {
		config = append(config, [2]string{"access_groups", h.AccessGroups})
	}
	var b strings.Builder
	b.Write(existing)
	b.WriteString("\n[connections.\"service/oidc\"]\nkind = \"service/oidc\"\nlabel = \"Sign-in\"\nconfig = {")
	for i, kv := range config {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, " %s = %q", kv[0], kv[1])
	}
	b.WriteString(" }\ncapabilities = [\"discovery\", \"client_auth\"]\n")
	if h.Secret != "" {
		secret := filepath.Join(dir, "oidc-client-secret")
		if err := os.WriteFile(secret, []byte(h.Secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "credential_files = { client_secret = %q }\n", secret)
	}
	path := filepath.Join(dir, "oidc-host.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (s *apiSite) token(name string, o ...oidctest.AccessOptions) string {
	var opts oidctest.AccessOptions
	if len(o) > 0 {
		opts = o[0]
	}
	return s.idp.AccessToken(name, apiClient, opts)
}

func (s *apiSite) do(method, path, token string, body any, headers ...string) apiReply {
	s.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			s.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, s.base+path, reader)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	out := apiReply{status: resp.StatusCode, header: resp.Header, body: string(data)}
	_ = json.Unmarshal(data, &out.json)
	return out
}

func expectAPI(t *testing.T, got apiReply, status int, code string) {
	t.Helper()
	if got.status != status || got.code() != code {
		t.Fatalf("status %d code %q, want %d %q: %.400s", got.status, got.code(), status, code, got.body)
	}
}

func (s *apiSite) events(token string, threadID, runID int64, lastID string) (string, string) {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.base+"/api/v1/threads/"+strconv.FormatInt(threadID, 10)+"/runs/"+strconv.FormatInt(runID, 10)+"/events", nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		s.t.Fatalf("event stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var all strings.Builder
	last := ""
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		line := scan.Text()
		all.WriteString(line + "\n")
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			last = id
		}
		if line == "event: done" {
			scan.Scan()
			all.WriteString(scan.Text() + "\n")
			break
		}
	}
	return all.String(), last
}

func number(t *testing.T, v any) int64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("not a number: %#v", v)
	}
	return int64(f)
}

func oidcBrowser(t *testing.T, base string, idp *oidctest.Provider, user string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code, body, _ := fetch(t, browser, http.MethodGet, base+"/login", nil); code != http.StatusOK {
		t.Fatalf("login page: %d %s", code, body)
	}
	idp.Approve(user)
	code, body, header := fetch(t, browser, http.MethodPost, base+"/login", url.Values{"next": {""}})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /login: %d %s", code, body)
	}
	code, body, header = fetch(t, browser, http.MethodGet, header.Get("Location"), nil)
	if code != http.StatusFound {
		t.Fatalf("authorize: %d %s", code, body)
	}
	if code, body, _ = fetch(t, browser, http.MethodGet, header.Get("Location"), nil); code != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", code, body)
	}
	return browser
}

func TestAPIRefusesRequestsWithoutAValidBearer(t *testing.T) {
	s := newAPISite(t)
	owner := s.token("owner")

	me := s.do(http.MethodGet, "/api/v1/me", owner, nil)
	if me.status != http.StatusOK || me.json["subject"] != "sub-owner" || me.json["email"] != "owner@example.com" || me.json["name"] != "owner" || me.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("me: %d %s %v", me.status, me.body, me.header)
	}
	if _, ok := me.json["admin"]; ok {
		t.Fatalf("me still reports an administrator field: %s", me.body)
	}
	if guest := s.do(http.MethodGet, "/api/v1/me", s.token("guest"), nil); guest.status != http.StatusOK || guest.json["subject"] != "sub-guest" {
		t.Fatalf("guest me: %d %s", guest.status, guest.body)
	}

	none := s.do(http.MethodGet, "/api/v1/me", "", nil)
	expectAPI(t, none, http.StatusUnauthorized, "unauthenticated")
	if none.header.Get("WWW-Authenticate") != `Bearer realm="atrium"` {
		t.Fatalf("WWW-Authenticate = %q", none.header.Get("WWW-Authenticate"))
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/me", "", nil, "Authorization", "Basic b3duZXI6cHc="), http.StatusUnauthorized, "unauthenticated")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/nowhere", "", nil), http.StatusUnauthorized, "unauthenticated")

	before := s.idp.JWKSCalls()
	forged := s.do(http.MethodGet, "/api/v1/me", s.token("owner", oidctest.AccessOptions{Foreign: true}), nil)
	expectAPI(t, forged, http.StatusUnauthorized, "invalid_token")
	if !strings.Contains(forged.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate = %q", forged.header.Get("WWW-Authenticate"))
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/me", s.token("owner", oidctest.AccessOptions{Foreign: true}), nil), http.StatusUnauthorized, "invalid_token")
	if calls := s.idp.JWKSCalls() - before; calls > 1 {
		t.Fatalf("unknown key ids fetched the JWKS %d times inside the refetch gap", calls)
	}

	for name, opts := range map[string]oidctest.AccessOptions{
		"web client audience": {Audience: []string{s.idp.ClientID()}, ClientID: s.idp.ClientID()},
		"foreign client_id":   {ClientID: "someone-else"},
		"id token":            {Typ: "JWT"},
		"other issuer":        {Issuer: "https://issuer.invalid"},
		"expired":             {ExpiresIn: -2 * time.Minute},
		"authorized party":    {Mutate: func(c map[string]any) { c["azp"] = "someone-else" }},
	} {
		t.Run(name, func(t *testing.T) {
			expectAPI(t, s.do(http.MethodGet, "/api/v1/me", s.token("owner", opts), nil), http.StatusUnauthorized, "invalid_token")
		})
	}
	if skew := s.do(http.MethodGet, "/api/v1/me", s.token("owner", oidctest.AccessOptions{ExpiresIn: -30 * time.Second}), nil); skew.status != http.StatusOK {
		t.Fatalf("a token 30 s past expiry is inside the clock skew: %d %s", skew.status, skew.body)
	}

	expectAPI(t, s.do(http.MethodGet, "/api/v1/me", s.token("stranger"), nil), http.StatusForbidden, "forbidden")
	if inbox := s.do(http.MethodGet, "/api/v1/inbox", s.token("guest"), nil); inbox.status != http.StatusOK {
		t.Fatalf("an admitted member cannot read the inbox API: %d %s", inbox.status, inbox.body)
	}

	expectAPI(t, s.do(http.MethodGet, "/api/v1/me", "", nil, "Remote-Groups", "atrium_access,atrium_admin", "Remote-User", "owner"), http.StatusUnauthorized, "unauthenticated")

	browser := oidcBrowser(t, s.base, s.idp, "owner")
	if code, _, _ := fetch(t, browser, http.MethodGet, s.base+"/settings/auth", nil); code != http.StatusOK {
		t.Fatalf("the web session does not work: %d", code)
	}
	code, body, _ := fetch(t, browser, http.MethodGet, s.base+"/api/v1/me", nil)
	if code != http.StatusUnauthorized || !strings.Contains(body, `"unauthenticated"`) {
		t.Fatalf("a session cookie authenticated the API: %d %s", code, body)
	}

	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{}, "Origin", "https://elsewhere.example"), http.StatusForbidden, "cross_origin")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", owner, "text=hi", "Content-Type", "application/x-www-form-urlencoded"), http.StatusUnsupportedMediaType, "unsupported_media_type")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", owner, `{"surprise":1}`), http.StatusBadRequest, "bad_request")
	if host := s.do(http.MethodGet, "/api/v1/me", owner, nil, "Host", "elsewhere.example"); host.status != http.StatusBadRequest {
		t.Fatalf("bad Host: %d %s", host.status, host.body)
	}

	s.idp.RevokeAll = true
	expectAPI(t, s.do(http.MethodGet, "/api/v1/me", s.token("owner"), nil), http.StatusUnauthorized, "invalid_token")
	s.idp.RevokeAll = false
}

func TestThreadTranscriptRevision(t *testing.T) {
	s := newAPISite(t)
	owner := s.token("owner")
	made := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "hello there"})
	if made.status != http.StatusCreated {
		t.Fatalf("create: %d %s", made.status, made.body)
	}
	thread, _ := made.json["thread"].(map[string]any)
	threadID := number(t, thread["id"])
	s.events(owner, threadID, number(t, made.json["run_id"]), "")
	path := "/api/v1/threads/" + strconv.FormatInt(threadID, 10)

	first := s.do(http.MethodGet, path, owner, nil)
	tag := first.header.Get("ETag")
	firstItems, _ := first.json["items"].([]any)
	if first.status != http.StatusOK || tag == "" || len(firstItems) < 2 {
		t.Fatalf("transcript: %d etag %q %s", first.status, tag, first.body)
	}
	if same := s.do(http.MethodGet, path, owner, nil, "If-None-Match", tag); same.status != http.StatusNotModified || same.body != "" || same.header.Get("ETag") != tag {
		t.Fatalf("unchanged transcript with its ETag = %d %q %s", same.status, same.header.Get("ETag"), same.body)
	}
	if paged := s.do(http.MethodGet, path+"?limit=1", owner, nil, "If-None-Match", tag); paged.status != http.StatusOK {
		t.Fatalf("a different page with the full transcript ETag = %d", paged.status)
	}

	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("again")))
	sent := s.do(http.MethodPost, path+"/messages", owner, map[string]any{"text": "once more"})
	if sent.status != http.StatusAccepted {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	s.events(owner, threadID, number(t, sent.json["run_id"]), "")
	changed := s.do(http.MethodGet, path, owner, nil, "If-None-Match", tag)
	items, _ := changed.json["items"].([]any)
	if changed.status != http.StatusOK || changed.header.Get("ETag") == tag || len(items) <= len(firstItems) {
		t.Fatalf("transcript after a new message with the old ETag = %d etag %q %s", changed.status, changed.header.Get("ETag"), changed.body)
	}
}

func TestAPILocalTokensWorkWithoutOIDC(t *testing.T) {
	moduleFixture(t, flagsExcept("", ""))
	issued := runApp(t, "auth", "token", "issue", "--name", "phone")
	if issued.code != cli.ExitOK {
		t.Fatalf("auth token issue: exit %d stdout %q stderr %q", issued.code, issued.stdout, issued.stderr)
	}
	secret := strings.TrimSpace(issued.stdout)
	if !strings.HasPrefix(secret, "atr_") {
		t.Fatalf("issue printed %q, not a token secret", issued.stdout)
	}
	for _, auth := range [][]string{{"--auth", "local"}, {"--auth", "proxy", "--allowed-host", "127.0.0.1"}} {
		base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, auth...)
		if err != nil {
			t.Fatal(err)
		}
		s := &apiSite{t: t, base: base}
		if me := s.do(http.MethodGet, "/api/v1/me", secret, nil); me.status != http.StatusOK || me.json["api_version"] != float64(1) || me.json["email"] != "" || me.json["name"] != "" {
			t.Fatalf("%v: a local token cannot read the API: %d %s", auth, me.status, me.body)
		}
		expectAPI(t, s.do(http.MethodGet, "/api/v1/me", "a.b.c", nil), http.StatusNotFound, "api_unavailable")
		expectAPI(t, s.do(http.MethodGet, "/api/v1/me", "", nil), http.StatusUnauthorized, "unauthenticated")
		expectAPI(t, s.do(http.MethodGet, "/api/v1/me", "atr_short", nil), http.StatusUnauthorized, "invalid_token")
		stop()
	}
}

func TestAPIChatSendStreamsTheRun(t *testing.T) {
	s := newAPISite(t)
	owner := s.token("owner")

	made := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "hello there"})
	if made.status != http.StatusCreated {
		t.Fatalf("create: %d %s", made.status, made.body)
	}
	thread, _ := made.json["thread"].(map[string]any)
	threadID, runID := number(t, thread["id"]), number(t, made.json["run_id"])

	stream, last := s.events(owner, threadID, runID, "")
	for _, want := range []string{"event: run_start", "event: text", `"hi"`, "event: run_done", "event: done"} {
		if !strings.Contains(stream, want) {
			t.Fatalf("stream lacks %q:\n%s", want, stream)
		}
	}
	replay, _ := s.events(owner, threadID, runID, last)
	if strings.Contains(replay, "event: text") || !strings.Contains(replay, "event: done") {
		t.Fatalf("Last-Event-ID replayed earlier events:\n%s", replay)
	}

	view := s.do(http.MethodGet, "/api/v1/threads/"+strconv.FormatInt(threadID, 10), owner, nil)
	items, _ := view.json["items"].([]any)
	if view.status != http.StatusOK || len(items) < 2 || view.json["active_run"] != nil {
		t.Fatalf("transcript: %d %s", view.status, view.body)
	}
	first, _ := items[0].(map[string]any)
	lastItem, _ := items[len(items)-1].(map[string]any)
	if first["kind"] != "user" || first["text"] != "hello there" || lastItem["kind"] != "assistant" || lastItem["text"] != "hi" {
		t.Fatalf("transcript items: %s", view.body)
	}
	page := s.do(http.MethodGet, "/api/v1/threads/"+strconv.FormatInt(threadID, 10)+"?limit=1", owner, nil)
	older := page.json["next_cursor"]
	pageItems, _ := page.json["items"].([]any)
	if len(pageItems) != 1 || older == nil {
		t.Fatalf("transcript page: %s", page.body)
	}
	back := s.do(http.MethodGet, "/api/v1/threads/"+strconv.FormatInt(threadID, 10)+"?limit=1&cursor="+older.(string), owner, nil)
	backItems, _ := back.json["items"].([]any)
	if len(backItems) != 1 || backItems[0].(map[string]any)["seq"] == pageItems[0].(map[string]any)["seq"] {
		t.Fatalf("older page: %s", back.body)
	}

	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("again")))
	path := "/api/v1/threads/" + strconv.FormatInt(threadID, 10) + "/messages"
	sent := s.do(http.MethodPost, path, owner, map[string]any{"text": "once more"}, "Idempotency-Key", "k-1")
	if sent.status != http.StatusAccepted || sent.json["run_id"] == nil {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	second := number(t, sent.json["run_id"])
	if stream, _ := s.events(owner, threadID, second, ""); !strings.Contains(stream, `"again"`) {
		t.Fatalf("second run stream:\n%s", stream)
	}
	replayed := s.do(http.MethodPost, path, owner, map[string]any{"text": "once more"}, "Idempotency-Key", "k-1")
	if replayed.status != http.StatusAccepted || replayed.body != sent.body || replayed.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %s %v", replayed.status, replayed.body, replayed.header)
	}
	expectAPI(t, s.do(http.MethodPost, path, owner, map[string]any{"text": "different"}, "Idempotency-Key", "k-1"), http.StatusUnprocessableEntity, "idempotency_mismatch")
	d := mustConnect(t, filepath.Join(s.dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var runs int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM runs WHERE thread_id = ?", threadID).Scan(&runs); err != nil || runs != 2 {
		t.Fatalf("runs = %d, %v; the replay must not start a run", runs, err)
	}

	expectAPI(t, s.do(http.MethodPost, path, owner, map[string]any{"text": "  "}), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads/999999/messages", owner, map[string]any{"text": "x"}), http.StatusNotFound, "not_found")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads/"+strconv.FormatInt(threadID, 10)+"/stop", owner, nil), http.StatusConflict, "no_active_run")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads/"+strconv.FormatInt(threadID, 10)+"/answer", owner, map[string]any{"text": "yes"}), http.StatusConflict, "no_question")

	if empty := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"web_search": false}); empty.status != http.StatusCreated || empty.json["run_id"] != nil {
		t.Fatalf("empty thread: %d %s", empty.status, empty.body)
	}
	list := s.do(http.MethodGet, "/api/v1/threads?limit=1", owner, nil)
	listed, _ := list.json["items"].([]any)
	cursor, _ := list.json["next_cursor"].(string)
	if list.status != http.StatusOK || len(listed) != 1 || cursor == "" {
		t.Fatalf("thread list: %s", list.body)
	}
	rest := s.do(http.MethodGet, "/api/v1/threads?limit=1&cursor="+cursor, owner, nil)
	restItems, _ := rest.json["items"].([]any)
	if len(restItems) != 1 || rest.json["next_cursor"] != nil || restItems[0].(map[string]any)["id"] == listed[0].(map[string]any)["id"] {
		t.Fatalf("thread list page 2: %s", rest.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/threads?cursor=garbage", owner, nil), http.StatusBadRequest, "bad_cursor")

	for _, title := range []string{"one", "two", "three"} {
		if _, err := d.Write.Exec("INSERT INTO notifications (ts, title, body) VALUES (?, ?, 'body')", float64(time.Now().Unix()), title); err != nil {
			t.Fatal(err)
		}
	}
	inbox := s.do(http.MethodGet, "/api/v1/inbox?limit=2", owner, nil)
	inboxItems, _ := inbox.json["items"].([]any)
	next, _ := inbox.json["next_cursor"].(string)
	if inbox.status != http.StatusOK || len(inboxItems) != 2 || inboxItems[0].(map[string]any)["title"] != "three" || next == "" {
		t.Fatalf("inbox: %d %s", inbox.status, inbox.body)
	}
	tail := s.do(http.MethodGet, "/api/v1/inbox?limit=2&cursor="+next, owner, nil)
	tailItems, _ := tail.json["items"].([]any)
	if len(tailItems) == 0 || tailItems[0].(map[string]any)["title"] != "one" {
		t.Fatalf("inbox page 2: %s", tail.body)
	}
}
