package pages

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type e2eSite struct {
	t     *testing.T
	db    *db.DB
	srv   *web.Server
	token string
}

func newE2E(t *testing.T, fetch money.Fetch) *e2eSite {
	t.Helper()
	d := dbtest.Schema(t)
	dir := t.TempDir()
	env := config.Map(map[string]string{"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "live"})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	render, err := web.NewRenderer(cfg, d, tpl.Config{Modules: []fs.FS{Templates(), reviewpages.Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	clock := 1_792_425_600.0
	srv, err := web.New(web.Options{
		Cfg: cfg, Env: env, DB: d, Auth: web.AuthLocal, AllowedHosts: []string{"testserver"}, Render: render,
		Modules: []web.Module{
			Module(Options{Now: func() float64 { clock++; return clock }, Fetch: fetch}),
			reviewpages.Module(reviewpages.Options{Writes: toolset.Undoers()}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &e2eSite{t: t, db: d, srv: srv}
	for _, c := range s.do(http.MethodGet, "/money", nil).Result().Cookies() {
		if c.Name == web.CSRFCookie {
			s.token = c.Value
		}
	}
	if s.token == "" {
		t.Fatal("no csrf cookie")
	}
	return s
}

func (s *e2eSite) do(method, target string, form url.Values) *httptest.ResponseRecorder {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	req.Host = "testserver"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method != http.MethodGet && s.token != "" {
		req.AddCookie(&http.Cookie{Name: web.CSRFCookie, Value: s.token})
		req.Header.Set(web.CSRFHeader, s.token)
	}
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}

func (s *e2eSite) status(method, target string, form url.Values, want int) string {
	s.t.Helper()
	rec := s.do(method, target, form)
	if rec.Code != want {
		s.t.Fatalf("%s %s = %d, want %d: %.300s", method, target, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

func (s *e2eSite) exec(query string, args ...any) int64 {
	s.t.Helper()
	res, err := s.db.Write.ExecContext(context.Background(), query, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *e2eSite) count(query string, args ...any) (n int) {
	s.t.Helper()
	if err := s.db.Read.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *e2eSite) text(query string, args ...any) (v string) {
	s.t.Helper()
	if err := s.db.Read.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		s.t.Fatal(err)
	}
	return v
}

func (s *e2eSite) seedTransaction(payee int64, account any, kind, date string, amount float64, currency, role string, message int64, description string) int64 {
	s.t.Helper()
	id := s.exec("INSERT INTO transactions (account_id, kind, date, payee_id, payee_raw, description, original_amount, original_currency, amount, currency, created_ts) VALUES (?, ?, ?, ?, 'RAW', ?, ?, ?, ?, ?, 1)",
		account, kind, date, payee, description, amount, currency, amount, currency)
	extracted := py.JSONDumps(money.Item{Kind: kind, PayeeRaw: "RAW", Amount: amount, Currency: currency, Date: date, Description: description}.Dict())
	s.exec("INSERT INTO transaction_sources (transaction_id, message_id, role, extracted, ts) VALUES (?, ?, ?, ?, 1)", id, message, role, extracted)
	return id
}

func form(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

func itoa(n int64) string { return py.ReprValue(n) }

func TestMoneyPagesRenderAndEdit(t *testing.T) {
	s := newE2E(t, nil)
	account := s.exec("INSERT INTO money_accounts (bank, name, kind, currency, last4, sender_patterns, forex, created_ts) VALUES ('republic', 'Republic Visa', 'credit_card', 'TTD', '1234', '[]', 0, 1)")
	groceries := s.exec("INSERT OR IGNORE INTO categories (name) VALUES ('Groceries')")
	anthropic := s.exec("INSERT INTO payees (name, created_ts) VALUES ('Anthropic', 1)")
	massy := s.exec("INSERT INTO payees (name, category_id, created_ts) VALUES ('Massy Stores', ?, 1)", groceries)
	alert := s.seedTransaction(anthropic, account, "purchase", "2026-10-12", 20, "TTD", money.BankAlert, 1, "")
	receipt := s.seedTransaction(anthropic, nil, "purchase", "2026-10-12", 20, "TTD", money.Receipt, 2, "")
	massyTx := s.seedTransaction(massy, account, "purchase", "2026-10-13", 300, "TTD", money.BankAlert, 3, "")
	late := s.seedTransaction(anthropic, nil, "purchase", "2026-10-20", 20, "USD", money.Receipt, 4, "late receipt")
	s.exec("UPDATE transactions SET amount = 135.3, currency = 'TTD' WHERE id = ?", late)

	if body := s.status(http.MethodGet, "/money", nil, 200); !strings.Contains(body, "late receipt") || !strings.Contains(body, "paid 20.00 USD") {
		t.Errorf("overview: %.400s", body)
	}
	if body := s.status(http.MethodGet, "/money/transactions?start=&end=&account_id=&payee=massy&category=&kind=&currency=", nil, 200); !strings.Contains(body, "Massy Stores") || strings.Contains(body, "Anthropic") {
		t.Errorf("filtered transactions: %.400s", body)
	}
	if body := s.status(http.MethodGet, "/money/transactions?unassigned=true", nil, 200); !strings.Contains(body, "late receipt") {
		t.Errorf("unassigned filter")
	}
	s.status(http.MethodGet, "/money/transactions?unassigned=maybe", nil, 422)
	s.status(http.MethodGet, "/money/payees", nil, 200)
	s.status(http.MethodGet, "/money/bills", nil, 200)
	if !strings.Contains(s.status(http.MethodGet, "/money/accounts", nil, 200), "Republic Visa") {
		t.Error("accounts page lacks the account")
	}

	s.status(http.MethodPost, "/money/transactions/merge", form("transaction_ids", itoa(alert), "transaction_ids", itoa(receipt)), 200)
	if n := s.count("SELECT COUNT(*) FROM transaction_sources WHERE transaction_id = ?", alert); n != 2 {
		t.Fatalf("merged sources = %d", n)
	}
	if s.count("SELECT COUNT(*) FROM agent_writes WHERE tool = 'transaction_merge' AND target = ?", "transaction:"+itoa(alert)) != 1 {
		t.Error("merge not recorded")
	}
	s.status(http.MethodPost, "/money/transactions/"+itoa(alert)+"/split", nil, 200)
	s.status(http.MethodPost, "/money/transactions/"+itoa(late)+"/account", form("account_id", itoa(account)), 200)
	s.status(http.MethodPost, "/money/transactions/"+itoa(late)+"/account", form("account_id", itoa(account), "back", "transactions"), 200)

	s.status(http.MethodPost, "/money/payees/"+itoa(massy)+"/rename", form("name", "Massy"), 200)
	if s.text("SELECT name FROM payees WHERE id = ?", massy) != "Massy" {
		t.Fatal("rename did not apply")
	}
	if !strings.Contains(s.status(http.MethodGet, "/activity?category=money", nil, 200), "payee_rename") {
		t.Error("rename is not on Activity")
	}
	writeID := s.text("SELECT id FROM agent_writes WHERE tool = 'payee_rename'")
	s.status(http.MethodPost, "/activity/write/"+writeID+"/undo", nil, 303)
	if s.text("SELECT name FROM payees WHERE id = ?", massy) != "Massy Stores" {
		t.Fatal("undo did not restore the name")
	}

	s.status(http.MethodPost, "/money/payees/"+itoa(massy)+"/override", form("override", "bill"), 200)
	s.status(http.MethodPost, "/money/bills/"+itoa(massy)+"/override", form("override", ""), 200)
	s.status(http.MethodPost, "/money/payees/"+itoa(massy)+"/aliases", form("pattern", "MASSY", "kind", "prefix"), 200)
	s.status(http.MethodPost, "/money/categories", form("name", "Pets"), 200)
	if !strings.Contains(s.status(http.MethodGet, "/money/payees", nil, 200), "Pets") {
		t.Error("new category missing")
	}
	s.status(http.MethodPost, "/money/transactions/"+itoa(massyTx)+"/currency", form("currency", "USD"), 200)
	if s.text("SELECT original_currency FROM transactions WHERE id = ?", massyTx) != "USD" {
		t.Error("currency not set")
	}
	s.status(http.MethodPost, "/money/transactions/"+itoa(massyTx)+"/dismiss", form("reason", "mom's card"), 200)
	if !strings.Contains(s.status(http.MethodGet, "/money/transactions?dismissed=true", nil, 200), "mom&#39;s card") {
		t.Error("dismissed row not listed")
	}
	s.status(http.MethodPost, "/money/transactions/"+itoa(massyTx)+"/restore", nil, 200)
	s.status(http.MethodPost, "/money/transactions/"+itoa(massyTx)+"/category", form("category_id", ""), 200)
	s.status(http.MethodPost, "/money/transactions/dismiss-before", form("before", "2026-10-13"), 200)
	if s.count("SELECT COUNT(*) FROM transactions WHERE dismissed != '' AND date < '2026-10-13'") == 0 {
		t.Error("dismiss-before dismissed nothing")
	}
	s.status(http.MethodPost, "/money/transactions/merge", form("transaction_ids", itoa(alert)), 422)
	s.status(http.MethodPost, "/money/transactions/merge", nil, 422)
	s.status(http.MethodPost, "/money/payees/x/rename", form("name", "n"), 422)
	s.status(http.MethodPost, "/money/categories/999/rename", form("name", "n"), 404)
}

func TestMoneyAccountsAddUpdateDeleteAndRateFetch(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "cbtt", "fx_rates_job").Exchanges)
	fetch := func(ctx context.Context, _ string) (string, error) { return httpx.GetText(ctx, srv.URL+"/", 0) }
	s := newE2E(t, fetch)
	if !strings.Contains(s.status(http.MethodGet, "/money", nil, 200), "No accounts yet") {
		t.Error("empty overview")
	}
	s.status(http.MethodPost, "/money/accounts", form("bank", "cibc", "name", "CIBC BBD", "kind", "bank", "currency", "BBD", "last4", "9999"), 200)
	id := s.text("SELECT id FROM money_accounts")
	if got := s.text("SELECT sender_patterns FROM money_accounts"); got != `["cibc.com", "cibcfcib.com"]` {
		t.Errorf("sender patterns = %s", got)
	}
	s.status(http.MethodPost, "/money/accounts/"+id, form("name", "CIBC Barbados", "last4", "9998", "sender_patterns", "cibcfcib.com", "forex", "1"), 200)
	if s.text("SELECT name FROM money_accounts") != "CIBC Barbados" || s.count("SELECT forex FROM money_accounts") != 1 {
		t.Error("update not applied")
	}
	s.status(http.MethodPost, "/money/accounts", form("bank", "nope", "name", "X", "kind", "bank", "currency", "TTD"), 422)
	s.status(http.MethodPost, "/money/since", form("since", "2026-01-01"), 200)
	s.status(http.MethodPost, "/money/since", form("since", "nope"), 422)
	s.status(http.MethodPost, "/money/rates/fetch", nil, 200)
	if s.count("SELECT COUNT(*) FROM fx_rates") != 1 {
		t.Error("rate not stored")
	}
	s.status(http.MethodPost, "/money/accounts/"+id+"/delete", nil, 200)
	s.status(http.MethodPost, "/money/accounts/99/delete", nil, 404)
}

func TestRateFetchFailureIs502(t *testing.T) {
	s := newE2E(t, func(context.Context, string) (string, error) { return "", &httpx.ServiceUnavailable{Msg: "down"} })
	body := s.status(http.MethodPost, "/money/rates/fetch", nil, http.StatusBadGateway)
	if !strings.Contains(body, "Rate fetch failed") || !strings.Contains(body, "down") {
		t.Errorf("body: %.300s", body)
	}
}
