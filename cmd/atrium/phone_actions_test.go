package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/proposals"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

func TestPhoneActionsPageShowsOutstandingAndRevokes(t *testing.T) {
	dir := t.TempDir()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	ctx := settings.With(context.Background(), settings.New())
	now := float64(time.Now().Unix())
	approve, _, err := actionlink.MintPair(ctx, d.Write, 1, "fp", now)
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, body, _ := fetch(t, client, http.MethodGet, base+"/settings/phone-actions", nil)
	if code != http.StatusOK {
		t.Fatalf("page = %d", code)
	}
	for _, want := range []string{"Phone actions", "1 outstanding link", "web only", "Revoke all"} {
		if !contains(body, want) {
			t.Fatalf("page misses %q", want)
		}
	}
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/settings/phone-actions/revoke", url.Values{}); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	_, body, _ = fetch(t, client, http.MethodGet, base+"/settings/phone-actions", nil)
	if !contains(body, "0 outstanding") {
		t.Fatalf("page after revoke misses the zero count")
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
	_, page, _ := fetch(t, client, http.MethodGet, base+"/settings/phone-actions", nil)
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

type phoneNotice struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Actions []struct {
		Action string `json:"action"`
		Label  string `json:"label"`
		URL    string `json:"url"`
		Method string `json:"method"`
	} `json:"actions"`
}

func TestPhoneEligibleProposalCreationNotifiesWithActionButtons(t *testing.T) {
	dir := t.TempDir()
	notices := make(chan phoneNotice, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n phoneNotice
		if err := json.NewDecoder(r.Body).Decode(&n); err == nil {
			select {
			case notices <- n:
			default:
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	setEnv(t, settingEnv(notify.KeyURL), srv.URL)
	_, _, stop := startServeStoppable(t, dir)
	defer stop()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	reg := settings.New()
	st := config.NewState(reg)
	ctx := config.With(settings.With(audit.With(context.Background(), "test", audit.CLI), reg), st)
	if err := notify.EnsureChannels(ctx, d.Write); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE ntfy_channels SET content = 'full' WHERE name = 'assistant'"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Set(ctx, d.Write, policy.EnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Set(ctx, d.Write, notify.KeyURL, srv.URL); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	if proposals.OnCreate == nil {
		t.Fatal("proposal create hook is not installed")
	}
	if _, _, err := proposals.Create(ctx, d, "reminder", nil, "Call the dentist", map[string]any{}, map[string]any{}, "fp-notify", now); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notices:
		if len(n.Actions) != 2 {
			t.Fatalf("actions = %#v", n.Actions)
		}
		if n.Actions[0].Label != "Approve" || n.Actions[1].Label != "Dismiss" {
			t.Fatalf("action labels = %#v", n.Actions)
		}
		if !strings.HasPrefix(n.Actions[0].URL, "/actions/") || n.Actions[0].Method != "POST" {
			t.Fatalf("approve action = %#v", n.Actions[0])
		}
		if strings.Contains(n.Message, "Call the dentist") {
			t.Fatalf("notification leaked the mail subject: %q", n.Message)
		}
		if !strings.Contains(n.Message, "Proposal #") {
			t.Fatalf("notification body = %q", n.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no phone notification was posted")
	}
}
