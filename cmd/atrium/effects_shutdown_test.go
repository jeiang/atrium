package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
)

func approveRequest(ctx context.Context, client *http.Client, base string, proposal int64) (*http.Response, error) {
	target := fmt.Sprintf("%s/proposals/%d/approve", base, proposal)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(url.Values{"all": {"1"}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range client.Jar.Cookies(req.URL) {
		if c.Name == "atrium_csrf" {
			req.Header.Set("X-CSRF-Token", c.Value)
		}
	}
	return client.Do(req)
}

func trackingProposal(t *testing.T, api *modelLog, d *db.DB, pkg int64) int64 {
	t.Helper()
	if _, err := d.Write.Exec("INSERT INTO packages (id, carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, "+
		"source_message_id, last_message_id, updated_ts, first_seen) VALUES (?, 'UPS', ?, 'Shop', 'shop', '', 'shipped', 'Shipped', 1, 1, 100, 100)",
		pkg, fmt.Sprintf("1Z999AA1012345678%d", pkg)); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse(fmt.Sprintf("l%d", pkg), "track_package", map[string]any{"package_id": pkg})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("asked")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "track the package"); ran.code != cli.ExitOK {
		t.Fatalf("track run: %d %s", ran.code, ran.stderr)
	}
	var proposal int64
	if err := d.Read.QueryRow("SELECT id FROM proposals WHERE kind = 'track_package' AND status = 'pending' ORDER BY id DESC LIMIT 1").Scan(&proposal); err != nil {
		t.Fatalf("no pending track_package proposal: %v", err)
	}
	return proposal
}

func settledTracking(t *testing.T, d *db.DB, proposal int64) (state string, settled bool) {
	t.Helper()
	var settledAt *float64
	if err := d.Read.QueryRow("SELECT state, settled_at FROM effects WHERE operation = 'track_package' AND direction = 'forward' AND proposal_id = ?",
		proposal).Scan(&state, &settledAt); err != nil {
		return "", false
	}
	return state, settledAt != nil
}

func TestCanceledAndShutdownHeldTrackerWritesSettleOnceWithoutLateRenewal(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	var posts atomic.Int32
	arrived := make(chan int32, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	first := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/public/v1/trackers" {
			n := posts.Add(1)
			arrived <- n
			gate := release
			if n == 1 {
				gate = first
			}
			select {
			case <-r.Context().Done():
				return
			case <-gate:
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":{"tracker":{"trackerId":"trk_%d","trackingNumber":"1Z999AA1012345678%d"}}}`, n, n)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"trackings":[]}}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(releaseAll)
	setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, srv.URL+"/public/v1", "good"))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	canceled := trackingProposal(t, api, d, 1)
	held := trackingProposal(t, api, d, 2)

	logs := &lockedBuffer{}
	logx.Setup(logs)
	base, _, stop := startServeApp(t, freshApp(f), logs)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-arrived
		cancel()
	}()
	if resp, err := approveRequest(ctx, client, base, canceled); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("the canceled approval returned %d instead of being canceled", resp.StatusCode)
	}
	time.Sleep(300 * time.Millisecond)
	if state, settled := settledTracking(t, d, canceled); settled && state != string(ledger.Unknown) {
		t.Fatalf("the canceled write settled as %q while the carrier still held it", state)
	}
	close(first)
	var state string
	waitFor(t, func() bool {
		var settled bool
		state, settled = settledTracking(t, d, canceled)
		return settled
	})
	if state != string(ledger.Done) && state != string(ledger.Unknown) {
		t.Fatalf("the canceled held write settled as %q, want done or unknown", state)
	}
	var status string
	waitFor(t, func() bool {
		return d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", canceled).Scan(&status) == nil && status != "approved"
	})
	code, body := hxFetch(t, client, http.MethodPost, base+fmt.Sprintf("/proposals/%d/approve", canceled), url.Values{"all": {"1"}})
	if code != http.StatusOK || !strings.Contains(body, "Not approved") {
		t.Fatalf("re-approving the canceled write = %d: %s, want a refusal", code, body)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("the canceled write reached the carrier %d times, want once", n)
	}

	answered := make(chan int, 1)
	go func() {
		resp, err := approveRequest(context.Background(), client, base, held)
		if err != nil {
			answered <- 0
			return
		}
		_ = resp.Body.Close()
		answered <- resp.StatusCode
	}()
	<-arrived
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	time.Sleep(500 * time.Millisecond)
	releaseAll()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop with a held write")
	}
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("the approval held across shutdown answered %d, want 200", code)
	}
	if state, settled := settledTracking(t, d, held); !settled || state != string(ledger.Done) {
		t.Fatalf("the write held across shutdown is %q (settled %v), want done", state, settled)
	}
	if n := posts.Load(); n != 2 {
		t.Fatalf("the carrier saw %d writes, want one per approval", n)
	}
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", held).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("the held proposal is %q (%v), want applied", status, err)
	}
	for _, bad := range []string{"database is closed", "could not renew", "could not drain"} {
		if strings.Contains(logs.String(), bad) {
			t.Fatalf("shutdown logged %q:\n%s", bad, logs.String())
		}
	}
}
