//go:build !modernc

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
)

type ntfyPublish struct {
	Topic, Title, Message, Auth string
	Priority                    int
}

type fakeNtfy struct {
	*httptest.Server
	mu        sync.Mutex
	open      bool
	failing   bool
	tokens    map[string]bool
	minted    []string
	revoked   []string
	published []ntfyPublish
}

const ntfyUser, ntfyPassword = "operator", "hunter2-never-stored"

func newFakeNtfy(t *testing.T) *fakeNtfy {
	f := &fakeNtfy{tokens: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/account/token" && r.Method == http.MethodPost:
			if user, pass, ok := r.BasicAuth(); !ok || user != ntfyUser || pass != ntfyPassword {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			token := fmt.Sprintf("tk_fake%d", len(f.minted))
			f.tokens[token] = true
			f.minted = append(f.minted, token)
			_, _ = io.WriteString(w, `{"token":"`+token+`","expires":0}`)
		case r.URL.Path == "/v1/account/token" && r.Method == http.MethodDelete:
			token := r.Header.Get("X-Token")
			delete(f.tokens, token)
			f.revoked = append(f.revoked, token)
		case r.Method == http.MethodGet && r.URL.Path != "/v1/health":
			if !f.open {
				w.WriteHeader(http.StatusForbidden)
			}
		case r.Method == http.MethodPost:
			var p struct {
				Topic, Title, Message string
				Priority              int
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			f.published = append(f.published, ntfyPublish{p.Topic, p.Title, p.Message, r.Header.Get("Authorization"), p.Priority})
			if f.failing {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeNtfy) set(open, failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open, f.failing = open, failing
}

func (f *fakeNtfy) last() (ntfyPublish, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.published) == 0 {
		return ntfyPublish{}, 0
	}
	return f.published[len(f.published)-1], len(f.published)
}

func TestNtfyChannelsMintTokensProbePrivacyAndTrackHealth(t *testing.T) {
	f := newFakeNtfy(t)
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv("ATRIUM_DB_KEY", rightKeyHex)
	t.Setenv(settingEnv(notify.KeyURL), f.URL)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	t.Setenv(settingEnv(notify.KeyTopic), "phase-a-topic")
	if got := runApp("settings", "list"); got.code != 0 {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	key, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"), db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, stmt := range []string{
		"INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)",
		"INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'claude-haiku-5-5', 'low', 'done', 0)",
	} {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Write.Exec("INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "+
		"VALUES (?, 1, 1, 'chat', 'claude-haiku-5-5', 'low', 1, 0, 0, 0, 1, 0, '<=100K', 130)", float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}

	base, logs := startServeLogged(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	get := func() string {
		t.Helper()
		code, body, _ := fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
		if code != http.StatusOK {
			t.Fatalf("notifications page: %d\n%s", code, logs.String())
		}
		return body
	}
	post := func(path string, form url.Values) (int, string) {
		t.Helper()
		get()
		if form == nil {
			form = url.Values{}
		}
		code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/notifications"+path, form)
		return code, body
	}
	channel := func(name string) (topic, privacy string, failures int) {
		t.Helper()
		if err := d.Read.QueryRow("SELECT topic, privacy, failures FROM ntfy_channels WHERE name = ?", name).Scan(&topic, &privacy, &failures); err != nil {
			t.Fatal(err)
		}
		return topic, privacy, failures
	}
	waitPublishes := func(n int) ntfyPublish {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if p, got := f.last(); got >= n {
				return p
			}
		}
		_, got := f.last()
		t.Fatalf("%d publishes, want %d", got, n)
		return ntfyPublish{}
	}

	budget := waitPublishes(1)
	if budget.Topic != "phase-a-topic" || budget.Title != "atrium: 80% of the monthly budget used" {
		t.Errorf("budget notice %+v, want one 80%% notice on the transferred system topic", budget)
	}
	page := get()
	for _, name := range notify.Channels {
		if topic, privacy, _ := channel(name); topic != "phase-a-topic" || privacy != notify.Private {
			t.Errorf("%s channel topic %q privacy %q, want the Phase A topic verified private", name, topic, privacy)
		}
	}
	if !strings.Contains(page, "ntfy://"+strings.TrimPrefix(f.URL, "http://")+"/phase-a-topic") || strings.Contains(page, "data-ntfy-sh") {
		t.Error("page lacks the subscribe link or shows the ntfy.sh warning for a self-hosted server")
	}

	if code, body := post("/account", url.Values{"username": {ntfyUser}, "password": {"wrong"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "refused the username or password") {
		t.Fatalf("bad password: %d", code)
	}
	if code, _ := post("/account", url.Values{"username": {ntfyUser}, "password": {ntfyPassword}}); code != http.StatusOK {
		t.Fatalf("connect: %d", code)
	}
	var tokens, leaked int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM secrets WHERE name LIKE 'ntfy_token_%'").Scan(&tokens); err != nil || tokens != 3 {
		t.Fatalf("%d stored tokens (%v), want 3", tokens, err)
	}
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM settings WHERE value LIKE ?1) + "+
		"(SELECT COUNT(*) FROM settings_audit WHERE COALESCE(old_value, '') || COALESCE(new_value, '') LIKE ?1) + "+
		"(SELECT COUNT(*) FROM secrets WHERE value LIKE ?1)", "%"+ntfyPassword+"%").Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("the password is stored (%d rows, %v)", leaked, err)
	}
	if strings.Contains(logs.String(), ntfyPassword) || strings.Contains(get(), "tk_fake") {
		t.Error("the password reached the logs or a token reached the page")
	}

	post("/channels/assistant/test", nil)
	if sent := waitPublishes(2); sent.Title != "atrium test" || sent.Auth != "Bearer "+f.minted[0] {
		t.Errorf("private test publish %+v, want full text with the assistant token", sent)
	}

	f.set(true, false)
	post("/probe", nil)
	if _, privacy, _ := channel(notify.Assistant); privacy != notify.Public {
		t.Errorf("assistant privacy %q after an open probe, want public", privacy)
	}
	post("/channels/assistant/test", nil)
	if sent := waitPublishes(3); sent.Title != "atrium" || strings.Contains(sent.Message, "Test message") {
		t.Errorf("public topic publish %+v, want the private title only", sent)
	}

	f.set(false, true)
	for range notify.FailingAfter {
		post("/channels/media/test", nil)
	}
	if _, _, failures := channel(notify.Media); failures != notify.FailingAfter {
		t.Errorf("media failures %d, want %d", failures, notify.FailingAfter)
	}
	if notice := waitPublishes(7); notice.Priority != 4 || notice.Topic != "phase-a-topic" {
		t.Errorf("health notice %+v, want one high-priority system publish", notice)
	}
	var health int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications WHERE channel = 'system' AND title = 'atrium: notifications failing'").Scan(&health); err != nil || health != 1 {
		t.Errorf("%d health notices in the inbox (%v), want 1", health, err)
	}
	if !strings.Contains(get(), "data-notify-failing") {
		t.Error("no failing banner after three failures")
	}

	var failed int64
	if err := d.Read.QueryRow("SELECT id FROM notifications WHERE channel = 'media' AND delivered = 0 ORDER BY id DESC LIMIT 1").Scan(&failed); err != nil {
		t.Fatal(err)
	}
	f.set(false, false)
	if code, _ := post(fmt.Sprintf("/%d/resend", failed), nil); code != http.StatusOK {
		t.Fatalf("resend: %d", code)
	}
	waitPublishes(8)
	post(fmt.Sprintf("/%d/resend", failed), nil)
	if _, n := f.last(); n != 8 {
		t.Errorf("%d publishes after a second resend, want the delivered notification sent once", n)
	}
	if strings.Contains(get(), "data-notify-failing") {
		t.Error("banner still shown after a successful delivery")
	}

	mediaToken := f.minted[1]
	post("/channels/media/reset", nil)
	if topic, _, _ := channel(notify.Media); !strings.HasPrefix(topic, "atrium-") || len(topic) != len("atrium-")+32 {
		t.Errorf("media topic %q after reset, want a new 128-bit topic", topic)
	}
	f.mu.Lock()
	revoked, live := f.revoked, f.tokens[mediaToken]
	f.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != mediaToken || live {
		t.Errorf("revoked %v (media token still valid: %v), want the media token revoked", revoked, live)
	}
}
