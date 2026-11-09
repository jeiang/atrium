package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/integrations/transport"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/proposals"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

func TestPhoneActionsControlsLiveOnTheNotificationsPage(t *testing.T) {
	dir := t.TempDir()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	db := filepath.Join(dir, "atrium.db")
	d := mustConnect(t, db)
	ctx := settings.With(context.Background(), settings.New())
	now := float64(time.Now().Unix())
	approve, _, err := actionlink.MintPair(ctx, d.Write, 1, "fp", now)
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, body, _ := fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
	if code != http.StatusOK {
		t.Fatalf("notifications page = %d", code)
	}
	for _, want := range []string{`id="phone-actions"`, "1 outstanding link", "web only", "Revoke all", `action="/settings/phone-actions"`} {
		if !contains(body, want) {
			t.Fatalf("notifications page misses %q", want)
		}
	}
	if strings.Contains(body, `href="/settings/phone-actions"`) {
		t.Error("the settings tabs still link the phone actions page")
	}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/phone-actions", nil); code == http.StatusOK {
		t.Errorf("GET /settings/phone-actions = %d, want it gone", code)
	}

	noFollow := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	code, _, header := fetch(t, noFollow, http.MethodPost, base+"/settings/phone-actions", url.Values{"enabled": {"1"}, "kind": {"reminder", "gmail_label_merge"}})
	if code != http.StatusSeeOther || header.Get("Location") != "/settings/notifications#phone-actions" {
		t.Fatalf("save = %d to %q", code, header.Get("Location"))
	}
	if v, _ := storedSetting(t, db, policy.EnabledKey); v != "true" {
		t.Fatalf("stored %s = %q", policy.EnabledKey, v)
	}
	if v, _ := storedSetting(t, db, policy.KindsKey); v != "reminder" {
		t.Fatalf("stored %s = %q, want the web-only kind dropped", policy.KindsKey, v)
	}
	_, body, _ = fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
	if !contains(body, `name="enabled" value="1" checked`) || !contains(body, `name="kind" value="reminder" checked`) || contains(body, `name="kind" value="memory" checked`) {
		t.Fatal("notifications page does not show the saved phone actions policy")
	}

	code, body, _ = fetch(t, client, http.MethodPost, base+"/settings/phone-actions/revoke", url.Values{})
	if code != http.StatusOK || !contains(body, `id="phone-actions"`) {
		t.Fatalf("revoke = %d, want the notifications page back", code)
	}
	if !contains(body, "0 outstanding") {
		t.Fatalf("notifications page after revoke misses the zero count")
	}
	if _, err := actionlink.Verify(ctx, d.Read, approve, now); err != actionlink.ErrInvalid {
		t.Fatalf("link after revoke = %v, want ErrInvalid", err)
	}
}

func TestActionLinkEndpointRefusesReplayAlterationRotationAndDisallowedKinds(t *testing.T) {
	dir := t.TempDir()
	base, logs, stop := startServeStoppable(t, dir)
	defer stop()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	reg := settings.New()
	ctx := settings.With(audit.With(context.Background(), "test", audit.CLI), reg)
	now := float64(time.Now().Unix())
	if err := reg.Set(ctx, d.Write, policy.EnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if pol, err := policy.Load(ctx, d.Read); err != nil || !pol.Allows("reminder") {
		t.Fatalf("policy = %#v, %v", pol, err)
	}
	insert := func(id int64, kind, fingerprint string) {
		t.Helper()
		if _, err := d.Write.Exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (?, ?, 'pending', ?, 'Call', ?)", id, kind, now, fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	post := func(token string) int {
		t.Helper()
		code, _, _ := fetch(t, client, http.MethodPost, base+"/actions/"+token, nil)
		return code
	}
	insert(1, "reminder", "fp1")
	_, dismiss, err := actionlink.MintPair(ctx, d.Write, 1, "fp1", now)
	if err != nil {
		t.Fatal(err)
	}
	_, page, _ := fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
	var enabled string
	_ = d.Read.QueryRow("SELECT value FROM settings WHERE key = ?", policy.EnabledKey).Scan(&enabled)
	code, postBody, _ := fetch(t, client, http.MethodPost, base+"/actions/"+dismiss, nil)
	if code != http.StatusOK {
		t.Fatalf("dismiss = %d body=%q setting=%q page_enabled=%v\n%s", code, postBody, enabled, contains(page, `name="enabled" value="1" checked`), logs.String())
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = 1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dismissed" {
		t.Fatalf("status = %s", status)
	}
	_, activity, _ := fetch(t, client, http.MethodGet, base+"/activity", nil)
	if !contains(activity, "approved via phone") {
		t.Fatalf("activity does not show the phone resolution")
	}
	approve, _, err := actionlink.MintPair(ctx, d.Write, 2, "fp2", now)
	if err != nil {
		t.Fatal(err)
	}
	insert(2, "reminder", "fp2")
	if code := post(approve); code != http.StatusOK && code != http.StatusInternalServerError {
		t.Fatalf("first approve = %d", code)
	}
	if code := post(approve); code != http.StatusGone {
		t.Fatalf("replay = %d, want 410", code)
	}
	altered := "A" + approve[1:]
	if code := post(altered); code != http.StatusBadRequest {
		t.Fatalf("altered = %d, want 400", code)
	}
	insert(3, "reminder", "fp3")
	expired, _, err := actionlink.MintPair(ctx, d.Write, 3, "fp3", now-13*3600)
	if err != nil {
		t.Fatal(err)
	}
	if code := post(expired); code != http.StatusGone {
		t.Fatalf("expired = %d, want 410", code)
	}
	insert(4, "reminder", "fp4")
	rotated, _, err := actionlink.MintPair(ctx, d.Write, 4, "fp4", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := actionlink.Rotate(ctx, d.Write); err != nil {
		t.Fatal(err)
	}
	if code := post(rotated); code != http.StatusBadRequest {
		t.Fatalf("rotated = %d, want 400", code)
	}
	insert(5, "gmail_label_merge", "fp5")
	denied, _, err := actionlink.MintPair(ctx, d.Write, 5, "fp5", now)
	if err != nil {
		t.Fatal(err)
	}
	if code := post(denied); code != http.StatusForbidden {
		t.Fatalf("disallowed kind = %d, want 403", code)
	}
	limited := false
	for i := 0; i < actionlink.RateLimit; i++ {
		if _, _, err := actionlink.MintPair(ctx, d.Write, int64(100+i), "fp", now); err == actionlink.ErrRate {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("the rate limit was never reached")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && (haystack == needle || contains(haystack[1:], needle) || haystack[:len(needle)] == needle)
}

func TestPhoneEligibleProposalCreationNotifiesWithActionButtons(t *testing.T) {
	dir := t.TempDir()
	push, browser := pushRig(t, dir)
	_, _, stop := startServeStoppable(t, dir)
	defer stop()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	browser.store(t, d)
	reg := settings.New()
	st := config.NewState(reg)
	ctx := config.With(settings.With(audit.With(context.Background(), "test", audit.CLI), reg), st)
	if err := notify.EnsureChannels(ctx, d.Write); err != nil {
		t.Fatal(err)
	}
	if err := reg.Set(ctx, d.Write, policy.EnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{notify.KeyPushSubject: "mailto:ops@example.test", transport.CAKey(notify.PushTransport): filepath.Join(dir, "push-ca.pem")} {
		if err := reg.Set(ctx, d.Write, key, value); err != nil {
			t.Fatal(err)
		}
	}
	now := float64(time.Now().Unix())
	if proposals.OnCreate == nil {
		t.Fatal("proposal create hook is not installed")
	}
	if _, _, err := proposals.Create(ctx, d, "reminder", nil, "Call the dentist", map[string]any{}, map[string]any{}, "fp-notify", now); err != nil {
		t.Fatal(err)
	}
	var n struct {
		Body    string `json:"body"`
		Actions []struct {
			Action string `json:"action"`
			Title  string `json:"title"`
			URL    string `json:"url"`
		} `json:"actions"`
	}
	waitFor(t, func() bool {
		var failure string
		_ = d.Read.QueryRow("SELECT COALESCE(error, '') FROM notifications WHERE title = 'atrium: reminder proposal' ORDER BY id DESC LIMIT 1").Scan(&failure)
		if failure != "" {
			t.Fatalf("phone notification not delivered: %s", failure)
		}
		return push.count() > 0
	})
	raw, err := json.Marshal(browser.decrypt(t, push.wait(t, 1).body))
	if err != nil || json.Unmarshal(raw, &n) != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(n.Actions) != 2 || n.Actions[0].Title != "Approve" || n.Actions[1].Title != "Dismiss" {
		t.Fatalf("actions = %#v", n.Actions)
	}
	if !strings.HasPrefix(n.Actions[0].URL, "/actions/") || n.Actions[0].Action == "" {
		t.Fatalf("approve action = %#v", n.Actions[0])
	}
	if strings.Contains(n.Body, "Call the dentist") || !strings.Contains(n.Body, "Proposal #") {
		t.Fatalf("notification body = %q", n.Body)
	}
}
