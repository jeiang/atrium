package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func (b *wimBrowser) meG5(headers ...string) wimReply {
	b.site.t.Helper()
	return b.get("/web/v1/me", append([]string{"Atrium-Client", "web", "Accept", "application/json"}, headers...)...)
}

func wimSignInURLG5(t *testing.T, got wimReply) string {
	t.Helper()
	if got.status != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %.300s", got.status, got.body)
	}
	return wimLoginURLG5(t, got.body)
}

func wimEmailG5(t *testing.T, got wimReply) string {
	t.Helper()
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200: %.300s", got.status, got.body)
	}
	var out struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal([]byte(got.body), &out); err != nil {
		t.Fatalf("me reply: %v %.300s", err, got.body)
	}
	return out.Email
}

func wimMeG5(t *testing.T, base string, headers ...string) (int, string, http.Header) {
	t.Helper()
	return wimGet(t, base+"/web/v1/me", append([]string{"Atrium-Client", "web", "Accept", "application/json"}, headers...)...)
}

func wimLoginURLG5(t *testing.T, body string) string {
	t.Helper()
	var out struct {
		SignIn struct {
			LoginURL string `json:"login_url"`
		} `json:"sign_in"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("signed-out reply: %v %.300s", err, body)
	}
	return out.SignIn.LoginURL
}

func localWebG5(t *testing.T, base string) *webClient {
	t.Helper()
	c := newWebClient(t, base, nil)
	c.session("local")
	return c
}

func subscribeG5(t *testing.T, base string, b pushBrowser) {
	t.Helper()
	form := b.form()
	got := localWebG5(t, base).do(http.MethodPost, "/web/v1/devices", map[string]any{"endpoint": form.Get("endpoint"), "p256dh": form.Get("p256dh"), "auth": form.Get("auth")})
	if got.status != http.StatusCreated {
		t.Fatalf("browser subscribe = %d %s", got.status, got.body)
	}
}

func asstServeG5(t *testing.T) (string, *modelLog, *db.DB, *webClient) {
	t.Helper()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	return dir, api, d, localWebG5(t, startServeIn(t, dir))
}

func startG5(c *webClient, text string) (int64, int64) {
	c.t.Helper()
	sum := sha256.Sum256([]byte(text))
	made := c.do(http.MethodPost, "/web/v1/threads", map[string]any{"text": text}, "Idempotency-Key", "g5-"+hex.EncodeToString(sum[:16]))
	if made.status != http.StatusCreated {
		c.t.Fatalf("start a thread: %d %s", made.status, made.body)
	}
	thread, _ := made.json["thread"].(map[string]any)
	return number(c.t, thread["id"]), number(c.t, made.json["run_id"])
}

func proxyWebG5(t *testing.T, s *apiSite) *webClient {
	t.Helper()
	c := newWebClient(t, s.base, proxyOwner{http.DefaultTransport})
	c.session("proxy")
	return c
}

func rawWebG5(c *webClient, method, path, contentType string, body io.Reader, headers ...string) *http.Request {
	c.t.Helper()
	template := c.request(method, path, nil, headers...)
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header = template.Header
	req.Host = template.Host
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Remote-User", "owner")
	req.Header.Set("Remote-Groups", "atrium_access,atrium_admin")
	for _, cookie := range c.http.Jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	return req
}

func activityHasEffectG5(got apiReply, operation string, effect int64) bool {
	data, _ := got.json["data"].(map[string]any)
	items, _ := data["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		ids, _ := item["effect_ids"].([]any)
		for _, id := range ids {
			if n, _ := id.(float64); item["operation"] == operation && int64(n) == effect {
				return true
			}
		}
	}
	return false
}

func changedIDG5(t *testing.T, got apiReply) int64 {
	t.Helper()
	if data, ok := got.json["data"].(map[string]any); ok {
		return number(t, data["id"])
	}
	return number(t, got.json["id"])
}

func knowledgeCreateG5(t *testing.T, c *webClient, fields map[string]any) int64 {
	t.Helper()
	got := c.do(http.MethodPost, "/web/v1/knowledge", fields)
	if got.status != http.StatusCreated {
		t.Fatalf("create knowledge = %d: %.300s", got.status, got.body)
	}
	return changedIDG5(t, got)
}
