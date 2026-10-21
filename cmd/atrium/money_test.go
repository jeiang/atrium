package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

var moneyClock = float64(time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("AST", -4*3600)).Unix())

type moneyRig struct {
	d   *db.DB
	app *cli.App
}

func newMoneyRig(t *testing.T, chatURL string) *moneyRig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d, path); err != nil {
		t.Fatal(err)
	}
	for i, account := range []string{"icloud", "gmail"} {
		if _, err := d.Write.Exec("INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", account, account); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Write.Exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (?, ?, 'box', 'box', 'filed')", i+1, account); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Write.Exec("INSERT INTO fx_rates (date, currency, selling, as_of, fetched_ts) VALUES ('2026-10-01', 'USD', 6.78, '2026-10-01', ?)", moneyClock); err != nil {
		t.Fatal(err)
	}
	day := float64(86400)
	dbtest.AddMessage(t, d, 1, "Order total: USD 42.50\nUSB-C cable", map[string]any{
		"subject": "Your Amazon.com order", "from_name": "Amazon.com", "from_addr": "auto-confirm@amazon.test", "from_domain": "amazon.test",
		"date_ts": moneyClock - day, "has_money": 1, "k_receipt": 1,
	})
	dbtest.AddMessage(t, d, 2, "Save 20% this weekend", map[string]any{
		"subject": "Weekend sale", "from_name": "Shop", "from_addr": "deals@shop.test", "from_domain": "shop.test",
		"date_ts": moneyClock - 2*day, "has_money": 1,
	})
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(filepath.Dir(path), "cache"))
	t.Setenv("ATRIUM_MAIL_MAIL_CHAT_URL", chatURL)
	t.Setenv("ATRIUM_CORE_TZ", "America/Port_of_Spain")
	app := cli.New("atrium", logx.Setup)
	app.Add(commands(nil, func() float64 { return moneyClock })...)
	return &moneyRig{d: d, app: app}
}

func (r *moneyRig) refresh(t *testing.T, args ...string) string {
	t.Helper()
	var out, errs bytes.Buffer
	if code := r.app.Run(context.Background(), append([]string{"money", "refresh"}, args...), &out, &errs); code != cli.ExitOK {
		t.Fatalf("money refresh exited %d: %s", code, errs.String())
	}
	return out.String()
}

func (r *moneyRig) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := r.d.Read.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMoneyRefreshSendsTheGoldenExtractionRequests(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "llm", "money_extraction").Exchanges)
	r := newMoneyRig(t, srv.URL+"/v1")
	if got, want := r.refresh(t), "{\n  \"rates\": null,\n  \"scanned\": 2,\n  \"posted\": 1\n}\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM transactions WHERE original_amount = 42.5 AND original_currency = 'USD'"); n != 1 {
		t.Errorf("%d transactions for the receipt, want 1", n)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM money_scans"); n != 2 {
		t.Errorf("%d scanned messages, want 2", n)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM agent_writes WHERE tool = 'money_post'"); n != 1 {
		t.Errorf("%d money_post writes, want 1", n)
	}
	if got, want := r.refresh(t), "{\n  \"rates\": null,\n  \"scanned\": 0,\n  \"posted\": 0\n}\n"; got != want {
		t.Errorf("second stdout = %q, want %q", got, want)
	}
}

func TestMoneyRefreshDefersWithoutMarkingWhenChatIsDown(t *testing.T) {
	r := newMoneyRig(t, "http://127.0.0.1:1/v1")
	if got, want := r.refresh(t), "{\n  \"rates\": null,\n  \"scanned\": 0,\n  \"posted\": 0\n}\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM money_scans"); n != 0 {
		t.Errorf("%d messages marked scanned while the model was down, want 0", n)
	}
}

func TestMoneyRefreshHandlesMalformedExtractions(t *testing.T) {
	replies := []string{"not json", `{"kind": "purchase", "payee_raw": "X", "amount": -5, "currency": "TTD", "date": "", "bank": "", "account_last4": "", "account_amount": null, "account_currency": "", "statement_total": null, "description": "", "subscription": false, "cadence": "unknown"}`}
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := replies[min(next, len(replies)-1)]
		next++
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	t.Cleanup(srv.Close)
	r := newMoneyRig(t, srv.URL+"/v1")
	if got, want := r.refresh(t, "--limit", "5"), "{\n  \"rates\": null,\n  \"scanned\": 1,\n  \"posted\": 0\n}\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM money_scans WHERE found = 0"); n != 2 {
		t.Errorf("%d messages marked scanned without a posting, want 2", n)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM transactions"); n != 0 {
		t.Errorf("%d transactions posted from malformed answers, want 0", n)
	}
}
