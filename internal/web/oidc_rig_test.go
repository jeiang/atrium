package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type rigOpts struct {
	preset   string
	idp      oidctest.Options
	settings map[string]string
	auth     []string
	bare     bool
	db       *db.DB
	public   func(*http.Request) bool
}

type rig struct {
	t   *testing.T
	db  *db.DB
	idp *oidctest.Provider
	svc *oidc.Service
	srv *httptest.Server
	url string
}

func probe(r chi.Router, _ *web.Deps) {
	who := func(w http.ResponseWriter, r *http.Request) {
		id, ok := web.IdentityOf(r.Context())
		_, _ = io.WriteString(w, "who="+boolText(ok)+" source="+id.Source+" admin="+boolText(id.Admin)+" sub="+id.Subject)
	}
	r.Get("/ping", who)
	r.Get("/settings/ping", who)
	r.Post("/settings/ping", who)
	r.Post("/live/resume", who)
	r.Get("/public/file", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "public") })
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	if o.auth == nil {
		o.auth = []string{web.AuthOIDC}
	}
	if o.idp.ClientSecret == "" {
		o.idp.ClientSecret = "s3cret"
	}
	idp := oidctest.New(t, o.idp)
	d := o.db
	if d == nil {
		d = dbtest.Schema(t)
	}
	listener := httptest.NewUnstartedServer(nil)
	base := "http://" + listener.Listener.Addr().String()
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
		"ATRIUM_MODE": "shadow", "ATRIUM_CORE_PUBLIC_URL": base,
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx := audit.With(context.Background(), "test", audit.CLI)
	if !o.bare {
		values := map[string]string{
			oidc.KeyIssuer: idp.Issuer(), oidc.KeyClientID: idp.ClientID(),
		}
		if o.preset != "" {
			values[oidc.KeyPreset] = o.preset
		}
		for k, v := range o.settings {
			values[k] = v
		}
		for k, v := range values {
			if err := settings.Default.Set(ctx, d.Write, k, v, 0); err != nil {
				t.Fatalf("set %s: %v", k, err)
			}
		}
		settings.Default.SetPins(secretPin{secrets.OIDCClientSecret: o.idp.ClientSecret})
		t.Cleanup(func() { settings.Default.SetPins(nil) })
	}
	svc := oidc.New(d, env)
	svc.KeyRefetchGap = 0
	render, err := web.NewRenderer(cfg, d, tpl.Config{})
	if err != nil {
		t.Fatal(err)
	}
	app, err := web.New(web.Options{
		Cfg: cfg, Env: env, DB: d, Auth: o.auth, AllowedHosts: []string{"127.0.0.1"}, Render: render,
		Modules: []web.Module{probe}, OIDC: svc, Public: o.public,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener.Config.Handler = app
	listener.Start()
	t.Cleanup(func() {
		listener.Close()
		app.Close()
	})
	return &rig{t: t, db: d, idp: idp, svc: svc, srv: listener, url: base}
}

type secretPin map[string]string

func (p secretPin) Pin(key string) (string, string, bool) {
	v, ok := p[key]
	return v, "test", ok
}

type reply struct {
	Status int
	Header http.Header
	Body   string
}

func (r reply) location() string { return r.Header.Get("Location") }

type browser struct {
	t    *testing.T
	base string
	http *http.Client
	jar  *cookiejar.Jar
}

func (r *rig) browser() *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return &browser{t: r.t, base: r.url, jar: jar, http: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) do(method, target string, body url.Values, headers ...string) reply {
	b.t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = b.base + target
	}
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(body.Encode())
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		b.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.base)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: string(data)}
}

func (b *browser) get(target string, headers ...string) reply {
	b.t.Helper()
	return b.do(http.MethodGet, target, nil, headers...)
}

func (b *browser) csrf() string {
	b.t.Helper()
	u, _ := url.Parse(b.base)
	find := func() string {
		for _, c := range b.jar.Cookies(u) {
			if c.Name == web.CSRFCookie {
				return c.Value
			}
		}
		return ""
	}
	if token := find(); token != "" {
		return token
	}
	b.get("/login")
	return find()
}

func (b *browser) post(target string, form url.Values, headers ...string) reply {
	b.t.Helper()
	values := url.Values{}
	for k, v := range form {
		values[k] = v
	}
	values.Set("csrf_token", b.csrf())
	return b.do(http.MethodPost, target, values, headers...)
}

func (b *browser) startLogin(next string, headers ...string) reply {
	b.t.Helper()
	return b.post("/login", url.Values{"next": {next}}, headers...)
}

func (b *browser) signIn(r *rig, user string, headers ...string) reply {
	b.t.Helper()
	r.idp.Approve(user)
	start := b.startLogin("", headers...)
	if start.Status != http.StatusSeeOther {
		b.t.Fatalf("POST /login: %d %s", start.Status, start.Body)
	}
	authz := b.get(start.location())
	if authz.Status != http.StatusFound {
		b.t.Fatalf("authorize: %d %s", authz.Status, authz.Body)
	}
	return b.get(authz.location(), headers...)
}

func (b *browser) mustSignIn(r *rig, user string) {
	b.t.Helper()
	if cb := b.signIn(r, user); cb.Status != http.StatusSeeOther {
		b.t.Fatalf("callback for %s: %d %s", user, cb.Status, cb.Body)
	}
}

func (r *rig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.Read.QueryRow(query, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) exec(query string, args ...any) {
	r.t.Helper()
	if _, err := r.db.Write.Exec(query, args...); err != nil {
		r.t.Fatal(err)
	}
}

func expect(t *testing.T, got reply, status int, contains ...string) {
	t.Helper()
	if got.Status != status {
		t.Fatalf("status %d, want %d; location %q body %.300q", got.Status, status, got.location(), got.Body)
	}
	for _, want := range contains {
		if !strings.Contains(got.Body, want) {
			t.Fatalf("body lacks %q: %.400q", want, got.Body)
		}
	}
}
