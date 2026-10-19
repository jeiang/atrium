package reviewpages

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/smtp/smtptest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const hx = "HX-Request"

var credentials = map[string]string{
	"ICLOUD_MAIL_USERNAME": "me",
	"ICLOUD_APP_PASSWORD":  "pw",
	"GMAIL_ADDRESS":        "me@gmail.test",
	"GMAIL_APP_PASSWORD":   "pw",
}

// site is the served pages over a migrated database, with the fakes of every outside service.
type site struct {
	t        *testing.T
	db       *db.DB
	cfg      config.Config
	env      config.Env
	srv      *web.Server
	token    string
	registry *proposals.Registry
	writes   *writes.Registry
	smtp     *smtptest.Server
	imap     *mailtest.FakeClient
	clock    float64
	store    *drafts.Store
}

type siteOption func(env map[string]string)

func newSite(t *testing.T, options ...siteOption) *site {
	t.Helper()
	d := dbtest.Schema(t)
	dir := t.TempDir()
	vars := map[string]string{"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "live"}
	for k, v := range credentials {
		vars[k] = v
	}
	for _, option := range options {
		option(vars)
	}
	env := config.Map(vars)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	s := &site{
		t: t, db: d, cfg: cfg, env: env, registry: proposals.NewRegistry(), writes: toolset.Undoers(env), smtp: &smtptest.Server{},
		imap: mailtest.NewFakeClient(env), clock: 1_000_000,
	}
	s.imap.AddBox("INBOX", mailtest.NewBox(1))
	s.imap.AddBox("Sent Messages", mailtest.NewBox(1), `\Sent`)
	open := func(ctx context.Context, account string) (mail.Client, error) {
		if err := s.imap.Connect(ctx); err != nil {
			return nil, err
		}
		return s.imap, nil
	}
	tick := func() float64 {
		s.clock++
		return s.clock
	}
	s.store = &drafts.Store{DB: d, Cfg: cfg, Now: tick}
	render, err := web.NewRenderer(cfg, d, tpl.Config{Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(web.Options{
		Cfg: cfg, Env: env, DB: d, Auth: web.AuthLocal, AllowedHosts: []string{"testserver"}, Render: render,
		Modules: []web.Module{Module(Options{Open: open, Now: tick, Writes: s.writes, Registry: s.registry, SMTP: s.smtp.Factory})},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.srv = srv
	s.token = s.fetchToken()
	return s
}

func (s *site) fetchToken() string {
	rec := s.do(http.MethodGet, "/proposals", nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.CSRFCookie {
			return c.Value
		}
	}
	s.t.Fatal("no csrf cookie")
	return ""
}

// do sends one request; headers are alternating names and values.
func (s *site) do(method, target string, form url.Values, headers ...string) *httptest.ResponseRecorder {
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
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}

func (s *site) get(target string, headers ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do(http.MethodGet, target, nil, headers...)
}

func (s *site) post(target string, form url.Values, headers ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	return s.do(http.MethodPost, target, form, headers...)
}

func (s *site) exec(query string, args ...any) {
	s.t.Helper()
	if _, err := s.db.Write.Exec(query, args...); err != nil {
		s.t.Fatal(err)
	}
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int) string {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body)
	}
	return rec.Body.String()
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("body misses %q", want)
		}
	}
}

func mustNotContain(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(body, bad) {
			t.Errorf("body contains %q", bad)
		}
	}
}

func compact(s string) string { return strings.Join(strings.Fields(s), "") }

func form(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Add(pairs[i], pairs[i+1])
	}
	return v
}

func message(id int) py.Dict {
	return py.Dict{
		{Key: "id", Value: int64(id)}, {Key: "subject", Value: "Subject " + itoa(id)}, {Key: "from_name", Value: "Shop"},
		{Key: "from_addr", Value: "s@shop.test"}, {Key: "date_ts", Value: 1.0},
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func group(name string, ids ...int) py.Dict {
	messages := make([]any, len(ids))
	for i, id := range ids {
		messages[i] = message(id)
	}
	return py.Dict{
		{Key: "name", Value: name}, {Key: "count", Value: int64(len(ids))},
		{Key: "senders", Value: []any{py.Dict{{Key: "label", Value: "shop.test"}, {Key: "count", Value: int64(len(ids))}}}},
		{Key: "terms", Value: []any{}}, {Key: "subjects", Value: []any{"Subject"}}, {Key: "messages", Value: messages},
	}
}

// propose creates a pending proposal and returns its id.
func (s *site) propose(kind, account, title string, preview any, fingerprint string) int64 {
	s.t.Helper()
	var acct *string
	if account != "" {
		acct = &account
	}
	id, ok, err := proposals.Create(s.t.Context(), s.db, kind, acct, title, py.Dict{}, preview, fingerprint, 1_000.0)
	if err != nil || !ok {
		s.t.Fatalf("create %s: %v %v", kind, ok, err)
	}
	return id
}

func (s *site) proposal(id int64) *proposals.Proposal {
	s.t.Helper()
	p, err := proposals.Get(s.t.Context(), s.db.Write, id)
	if err != nil || p == nil {
		s.t.Fatalf("get proposal %d: %v %v", id, p, err)
	}
	return p
}

func (s *site) splitProposal(fingerprint string) int64 {
	preview := py.Dict{{Key: "folder", Value: "INBOX/Shop"}, {Key: "groups", Value: []any{group("Shop/Orders", 1, 2), group("Shop/Ads", 3, 4)}}}
	return s.propose("rebalance_split", "icloud", "Split Shop", preview, fingerprint)
}

func jsonOf(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	return out
}

// text is the visible text of a page: every tag replaced by a space, whitespace collapsed.
func text(body string) string {
	var b strings.Builder
	inTag := false
	for _, r := range body {
		switch {
		case r == '<':
			inTag = true
			b.WriteByte(' ')
		case r == '>' && inTag:
			inTag = false
			b.WriteByte(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// between is the part of s after the first start and before the next end.
func between(s, start, end string) string {
	_, rest, _ := strings.Cut(s, start)
	out, _, _ := strings.Cut(rest, end)
	return out
}

// optionValues are the value attributes of the options of an HTML fragment.
func optionValues(s string) []string {
	var out []string
	for _, part := range strings.Split(s, `<option value="`)[1:] {
		value, _, _ := strings.Cut(part, `"`)
		out = append(out, value)
	}
	return out
}
