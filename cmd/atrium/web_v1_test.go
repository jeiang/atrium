package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

type webClient struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func newWebClient(t *testing.T, base string, transport http.RoundTripper) *webClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &webClient{t: t, base: base, http: &http.Client{Jar: jar, Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *webClient) request(method, path string, body any, headers ...string) *http.Request {
	c.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Atrium-Client", "web")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", c.base)
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		switch {
		case headers[i] == "Host":
			req.Host = headers[i+1]
		case headers[i+1] == "":
			req.Header.Del(headers[i])
		default:
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	return req
}

func (c *webClient) do(method, path string, body any, headers ...string) apiReply {
	c.t.Helper()
	resp, err := c.http.Do(c.request(method, path, body, headers...))
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	out := apiReply{status: resp.StatusCode, header: resp.Header, body: string(data)}
	_ = json.Unmarshal(data, &out.json)
	return out
}

func (c *webClient) session(mode string) map[string]any {
	c.t.Helper()
	got := c.do(http.MethodGet, "/web/v1/session", nil)
	token, _ := got.json["csrf_token"].(string)
	if got.status != http.StatusOK || got.json["auth_mode"] != mode || token == "" {
		c.t.Fatalf("%s session: %d %s", mode, got.status, got.body)
	}
	if cookie := c.cookie("atrium_csrf"); cookie != token {
		c.t.Fatalf("the session csrf_token %q is not the CSRF cookie %q", token, cookie)
	}
	c.csrf = token
	return got.json
}

func (c *webClient) cookie(name string) string {
	u, _ := url.Parse(c.base)
	for _, cookie := range c.http.Jar.Cookies(u) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (c *webClient) run(text string) (int64, int64) {
	c.t.Helper()
	made := c.do(http.MethodPost, "/web/v1/threads", map[string]any{"text": text}, "Idempotency-Key", "web-"+text)
	if made.status != http.StatusCreated {
		c.t.Fatalf("create on %s: %d %s", c.base, made.status, made.body)
	}
	thread, _ := made.json["thread"].(map[string]any)
	return number(c.t, thread["id"]), number(c.t, made.json["run_id"])
}

func (c *webClient) events(threadID, runID int64) string {
	c.t.Helper()
	resp, err := c.http.Do(c.request(http.MethodGet, "/web/v1/threads/"+strconv.FormatInt(threadID, 10)+"/runs/"+strconv.FormatInt(runID, 10)+"/events", nil))
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, _ := io.ReadAll(resp.Body)
		c.t.Fatalf("web event stream on %s: %d %s", c.base, resp.StatusCode, body)
	}
	var all strings.Builder
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		all.WriteString(scan.Text() + "\n")
		if scan.Text() == "event: done" {
			break
		}
	}
	return all.String()
}

func TestWebChatPageReads(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	s := startAPISite(t, dir, api)
	c := newWebClient(t, s.base, nil)
	c.http = oidcBrowser(t, s.base, s.idp, "owner")
	c.session("oidc")

	cfg := c.do(http.MethodGet, "/web/v1/assistant/configuration", nil)
	defaults, _ := cfg.json["defaults"].(map[string]any)
	models, _ := cfg.json["models"].([]any)
	if cfg.status != http.StatusOK || defaults["model"] == "" || defaults["effort"] == "" || len(models) == 0 || cfg.json["composer"] == nil {
		t.Fatalf("configuration on /web/v1: %d %s", cfg.status, cfg.body)
	}
	expectAPI(t, c.do(http.MethodGet, "/web/v1/composer-options", nil), http.StatusUnprocessableEntity, "invalid")

	api.Push(anthropictest.Reply("end_turn", anthropictest.Thinking("weighing it", "sig"), anthropictest.Text("thought it through")))
	if stream := c.events(c.run("plain reply")); !strings.Contains(stream, `"hi"`) {
		t.Fatalf("plain run stream:\n%s", stream)
	}
	thread, run := c.run("think first")
	if stream := c.events(thread, run); !strings.Contains(stream, "event: run_done") {
		t.Fatalf("thinking run stream:\n%s", stream)
	}
	view := c.do(http.MethodGet, "/web/v1/threads/"+strconv.FormatInt(thread, 10), nil)
	items := transcriptItems(view)
	if view.status != http.StatusOK || len(items) != 3 {
		t.Fatalf("thinking transcript: %d %s", view.status, view.body)
	}
	user, activity, reply := items[0], items[1], items[2]
	steps, _ := activity["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("thinking activity steps: %s", view.body)
	}
	step, _ := steps[0].(map[string]any)
	results, isList := step["results"].([]any)
	if user["kind"] != "user" || activity["kind"] != "activity" || reply["kind"] != "assistant" || reply["text"] != "thought it through" ||
		step["kind"] != "thinking" || step["id"] != nil || !isList || len(results) != 0 ||
		number(t, activity["seq"]) != number(t, reply["seq"]) || number(t, user["seq"]) >= number(t, reply["seq"]) {
		t.Fatalf("a thinking-only reply is not an activity and an assistant item on one seq: %s", view.body)
	}
}

func TestWebSurfaceAdmitsOnlyTheWebSession(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	port := handOverEmergency(t)
	s := startAPISite(t, dir, api, "--emergency-port", port)
	owner := s.token("owner")

	anon := newWebClient(t, s.base, nil)
	out := anon.do(http.MethodGet, "/web/v1/session", nil)
	expectAPI(t, out, http.StatusUnauthorized, "unauthenticated")
	signIn, _ := out.json["sign_in"].(map[string]any)
	if signIn["login_url"] != "/login" || signIn["csrf_token"] == "" || signIn["csrf_token"] != anon.cookie("atrium_csrf") || !strings.Contains(out.body, `"modes":["proxy","oidc"]`) {
		t.Fatalf("signed-out session: %s", out.body)
	}
	expectAPI(t, anon.do(http.MethodGet, "/web/v1/me", nil, "Authorization", "Bearer "+owner), http.StatusBadRequest, "bearer_refused")
	expectAPI(t, anon.do(http.MethodPost, "/web/v1/threads", map[string]any{}, "Authorization", "Bearer "+owner), http.StatusBadRequest, "bearer_refused")
	if me := s.do(http.MethodGet, "/api/v1/me", owner, nil); me.status != http.StatusOK {
		t.Fatalf("the bearer no longer works on /api/v1: %d %s", me.status, me.body)
	}
	if path := anon.do(http.MethodGet, "/web/v1/nowhere", nil); path.status != http.StatusUnauthorized || path.header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unknown /web/v1 path while signed out: %d %v %s", path.status, path.header, path.body)
	}

	c := newWebClient(t, s.base, nil)
	c.http = oidcBrowser(t, s.base, s.idp, "owner")
	sess := c.session("oidc")
	identity, _ := sess["identity"].(map[string]any)
	signOut, _ := sess["sign_out"].(map[string]any)
	if identity["email"] != "owner@example.com" || identity["subject"] != "sub-owner" || signOut["method"] != "POST" || signOut["url"] != "/web/v1/session/logout" {
		t.Fatalf("OIDC session: %v", sess)
	}
	expectAPI(t, c.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, "unauthenticated")
	if me := c.do(http.MethodGet, "/web/v1/me", nil); me.status != http.StatusOK || me.json["email"] != "owner@example.com" || me.header.Get("Atrium-Request-Id") == "" {
		t.Fatalf("the session does not work on /web/v1: %d %s", me.status, me.body)
	}
	expectAPI(t, c.do(http.MethodGet, "/web/v1/me", nil, "Atrium-Client", ""), http.StatusForbidden, "cross_origin")
	expectAPI(t, c.do(http.MethodGet, "/web/v1/me", nil, "Sec-Fetch-Site", "cross-site"), http.StatusForbidden, "cross_origin")
	expectAPI(t, c.do(http.MethodGet, "/web/v1/me", nil, "Sec-Fetch-Site", "same-site"), http.StatusForbidden, "cross_origin")
	start := map[string]any{"web_search": false}
	expectAPI(t, c.do(http.MethodPost, "/web/v1/threads", start, "X-CSRF-Token", ""), http.StatusForbidden, "csrf_failed")
	expectAPI(t, c.do(http.MethodPost, "/web/v1/threads", start, "X-CSRF-Token", "stale"), http.StatusForbidden, "csrf_failed")
	expectAPI(t, c.do(http.MethodPost, "/web/v1/threads", start, "Origin", "https://elsewhere.example"), http.StatusForbidden, "cross_origin")
	expectAPI(t, c.do(http.MethodPost, "/web/v1/threads", start, "Origin", ""), http.StatusForbidden, "cross_origin")
	expectAPI(t, c.do(http.MethodPost, "/web/v1/threads", start, "Origin", "", "Sec-Fetch-Site", "cross-site"), http.StatusForbidden, "cross_origin")
	expectAPI(t, c.do(http.MethodPost, "/web/v1/threads", "text=hi", "Content-Type", "application/x-www-form-urlencoded"), http.StatusUnsupportedMediaType, "unsupported_media_type")
	if empty := c.do(http.MethodPost, "/web/v1/threads", start, "Origin", "", "Sec-Fetch-Site", "same-origin"); empty.status != http.StatusCreated {
		t.Fatalf("a same-origin fetch without Origin was refused: %d %s", empty.status, empty.body)
	}
	if host := c.do(http.MethodGet, "/web/v1/me", nil, "Host", "elsewhere.example"); host.status != http.StatusBadRequest {
		t.Fatalf("bad Host on /web/v1: %d %s", host.status, host.body)
	}

	expectAPI(t, c.do(http.MethodGet, "/web/v1/today", nil), http.StatusMethodNotAllowed, "method_not_allowed")
	today := c.do(http.MethodPost, "/web/v1/today", nil)
	if today.status != http.StatusOK || today.header.Get("Atrium-Command-Id") == "" {
		t.Fatalf("POST /web/v1/today: %d %s", today.status, today.body)
	}

	if stream := c.events(c.run("hello over oidc")); !strings.Contains(stream, `"hi"`) || !strings.Contains(stream, "event: run_done") {
		t.Fatalf("OIDC stream on /web/v1:\n%s", stream)
	}

	proxy := newWebClient(t, s.base, proxyOwner{http.DefaultTransport})
	if sess := proxy.session("proxy"); sess["sign_out"] != nil {
		t.Fatalf("a proxy identity was offered an atrium sign-out: %v", sess)
	}

	local := newWebClient(t, "http://127.0.0.1:"+port, nil)
	if sess := local.session("local"); sess["sign_out"] != nil {
		t.Fatalf("the local identity was offered a sign-out: %v", sess)
	}
	expectAPI(t, local.do(http.MethodGet, "/web/v1/me", nil, "Authorization", "Bearer "+owner), http.StatusBadRequest, "bearer_refused")
	expectAPI(t, local.do(http.MethodPost, "/web/v1/threads", start, "X-CSRF-Token", ""), http.StatusForbidden, "csrf_failed")
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("from local")))
	if stream := local.events(local.run("hello over the emergency listener")); !strings.Contains(stream, `"from local"`) {
		t.Fatalf("local stream on /web/v1:\n%s", stream)
	}

	old := c.cookie("atrium_session")
	if old == "" {
		t.Fatal("the OIDC browser holds no session cookie")
	}
	if code, _, _ := fetch(t, c.http, http.MethodGet, s.base+"/settings/auth", nil); code != http.StatusOK {
		t.Fatalf("the HTML session does not work before logout: %d", code)
	}
	expectAPI(t, c.do(http.MethodPost, "/web/v1/session/logout", nil, "X-CSRF-Token", ""), http.StatusForbidden, "csrf_failed")
	if out := c.do(http.MethodPost, "/web/v1/session/logout", nil); out.status != http.StatusNoContent || c.cookie("atrium_session") != "" {
		t.Fatalf("logout: %d %s, cookie %q", out.status, out.body, c.cookie("atrium_session"))
	}
	expectAPI(t, c.do(http.MethodGet, "/web/v1/session", nil), http.StatusUnauthorized, "unauthenticated")
	if code, _, header := fetch(t, c.http, http.MethodGet, s.base+"/settings/auth", nil); code != http.StatusSeeOther || !strings.HasPrefix(header.Get("Location"), "/login") {
		t.Fatalf("the HTML UI still admits the browser after logout: %d %s", code, header.Get("Location"))
	}
	replayed := newWebClient(t, s.base, nil)
	u, _ := url.Parse(s.base)
	replayed.http.Jar.SetCookies(u, []*http.Cookie{{Name: "atrium_session", Value: old}})
	ended := replayed.do(http.MethodGet, "/web/v1/session", nil)
	expectAPI(t, ended, http.StatusUnauthorized, "unauthenticated")
	if signIn, _ := ended.json["sign_in"].(map[string]any); !strings.HasPrefix(signIn["login_url"].(string), "/login?reason=") {
		t.Fatalf("the ended session cookie: %s", ended.body)
	}
	if code, _, _ := fetch(t, replayed.http, http.MethodGet, s.base+"/settings/auth", nil); code != http.StatusSeeOther {
		t.Fatalf("the ended session cookie still opens the HTML UI: %d", code)
	}
}
