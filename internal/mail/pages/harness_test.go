package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const host = "testserver"

var hx = [2]string{"HX-Request", "true"}

type ids struct{ invoice, other int64 }

type site struct {
	t      *testing.T
	db     *db.DB
	cfg    config.Config
	env    config.Env
	srv    *web.Server
	ids    ids
	cookie string
	token  string
}

func newEnv(t *testing.T, mode string) (config.Config, config.Env, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	env := config.Map(map[string]string{"ATRIUM_MODE": mode, "ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache")})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d, path); err != nil {
		t.Fatal(err)
	}
	mailtest.PinMode(t, mode)
	for i, account := range []string{"icloud", "gmail"} {
		rulestest.Exec(t, d, "INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", account, account)
		rulestest.Exec(t, d, "INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (?, ?, 'box', 'box', 'filed')", i+1, account)
	}
	return cfg, env, d
}

func newServer(t *testing.T, cfg config.Config, env config.Env, d *db.DB, o Options) *web.Server {
	t.Helper()
	render, err := web.NewRenderer(cfg, d, tpl.Config{Modules: []fs.FS{Templates()}, Globals: Globals()})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(web.Options{
		Cfg: cfg, Env: env, DB: d, Auth: web.AuthLocal, AllowedHosts: []string{host}, Render: render, Modules: []web.Module{Module(o)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func seeded(t *testing.T, mode string, o Options) *site {
	t.Helper()
	cfg, env, d := newEnv(t, mode)
	for i := range 3 {
		rulestest.Add(t, d, i+1, nil, "statement body", rulestest.Fields{
			"from_addr": "pay@bank.test", "from_domain": "bank.test", "folder": "Bills", "location": "Bills", "in_inbox": 0,
			"subject": fmt.Sprintf("Statement %d", i),
		})
	}
	invoice := rulestest.Add(t, d, 10, rulestest.Vector(0), "invoice due friday", rulestest.Fields{
		"seen": 1, "flagged": 0, "in_inbox": 1, "location": "INBOX", "from_addr": "billing@shop.test", "from_domain": "shop.test",
		"subject": "Invoice <b>due</b>", "from_name": "Shop", "date_ts": 1_700_000_000.0,
	})
	other := rulestest.Add(t, d, 11, nil, "", rulestest.Fields{
		"seen": 1, "flagged": 0, "in_inbox": 1, "location": "INBOX", "from_addr": "news@list.test", "from_domain": "list.test",
		"subject": "Weekly news", "date_ts": 1_700_100_000.0,
	})
	for _, id := range []int64{invoice, other} {
		rulestest.Exec(t, d, "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "+
			"VALUES (?, 'icloud', 1.0, 'operator', 'r-1', 'file', 'Bills', 1.0, '{}')", id)
	}
	rulestest.Exec(t, d, "INSERT INTO flag_decisions (message_id, account, ts, contacts_match, llm_flag) VALUES (?, 'icloud', 1.0, 1, 1)", invoice)
	s := &site{t: t, db: d, cfg: cfg, env: env, srv: newServer(t, cfg, env, d, o), ids: ids{invoice, other}}
	s.handshake()
	return s
}

func (s *site) handshake() {
	rec := s.do("GET", "/rules", nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.CSRFCookie {
			s.cookie, s.token = c.Name+"="+c.Value, c.Value
		}
	}
	if s.token == "" {
		s.t.Fatal("no CSRF cookie issued")
	}
}

func (s *site) do(method, path string, form url.Values, headers ...[2]string) *httptest.ResponseRecorder {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = host
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if s.cookie != "" {
		req.Header.Set("Cookie", s.cookie)
		req.Header.Set(web.CSRFHeader, s.token)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}

func (s *site) get(path string, headers ...[2]string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do("GET", path, nil, headers...)
}

func (s *site) post(path string, form url.Values, headers ...[2]string) *httptest.ResponseRecorder {
	s.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	return s.do("POST", path, form, headers...)
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int, label string) string {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d: %.1500s", label, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

func contains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("response misses %q:\n%s", want, body)
		}
	}
}

func lacks(t *testing.T, body string, nots ...string) {
	t.Helper()
	for _, not := range nots {
		if strings.Contains(body, not) {
			t.Errorf("response has %q:\n%s", not, body)
		}
	}
}

func (s *site) rows(query string, args ...any) [][]any {
	s.t.Helper()
	rows, err := s.db.Read.Query(query, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			s.t.Fatal(err)
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
	}
	return out
}

func (s *site) column(query string, args ...any) []string {
	s.t.Helper()
	var out []string
	for _, row := range s.rows(query, args...) {
		out = append(out, fmt.Sprint(row[0]))
	}
	return out
}

func mustJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %q", body)
	}
	return out
}

func (s *site) id(n int64) string { return fmt.Sprint(n) }
