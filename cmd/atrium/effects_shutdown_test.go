package main

import (
	"context"
	"fmt"
	"net"
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

func TestHeldTrackerWriteSettlesOnceAcrossServeShutdown(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	var posts atomic.Int32
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/public/v1/trackers" {
			posts.Add(1)
			arrived <- struct{}{}
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"tracker":{"trackerId":"trk_1","trackingNumber":"1Z999AA10123456781"}}}`))
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
	held := trackingProposal(t, api, d, 1)

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
	addr := strings.TrimPrefix(base, "http://")
	waitFor(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	})
	select {
	case <-stopped:
		t.Fatal("serve stopped before the held write was released")
	default:
	}
	if state, _ := trackingState(t, d, held); state != string(ledger.Intent) {
		t.Fatalf("the held write is %q while serve stops, want intent", state)
	}
	releaseAll()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop with a held write")
	}
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("the approval held across shutdown answered %d, want 200", code)
	}
	if state, settled := trackingState(t, d, held); !settled || state != string(ledger.Done) {
		t.Fatalf("the write held across shutdown is %q (settled %v), want done", state, settled)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("the carrier saw %d writes, want one", n)
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", held).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("the held proposal is %q (%v), want applied", status, err)
	}
	for _, bad := range []string{"database is closed", "could not drain"} {
		if strings.Contains(logs.String(), bad) {
			t.Fatalf("shutdown logged %q:\n%s", bad, logs.String())
		}
	}
}

func trackingState(t *testing.T, d *db.DB, proposal int64) (state string, settled bool) {
	t.Helper()
	var settledAt *float64
	if err := d.Read.QueryRow("SELECT state, settled_at FROM effects WHERE operation = 'track_package' AND direction = 'forward' AND proposal_id = ?",
		proposal).Scan(&state, &settledAt); err != nil {
		return "", false
	}
	return state, settledAt != nil
}
