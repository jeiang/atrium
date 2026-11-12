package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/module"
)

type moduleCase struct {
	module  string
	tool    string
	table   string
	method  string
	path    string
	form    url.Values
	status  int
	command []string
	output  func(stdout, stderr string, code int) bool
}

func nonEmpty(stdout, _ string, code int) bool { return code == cli.ExitOK && stdout != "" }

var moduleCases = []moduleCase{
	{module: module.Mail, tool: "mail_search", table: "messages", method: "GET", path: "/triage", status: http.StatusOK,
		command: []string{"rules", "export"}, output: nonEmpty},
	{module: module.Calendar, tool: "calendar_list", table: "agent_events",
		command: []string{"calendar", "candidates"}, output: nonEmpty},
	{module: module.Notion, table: "notion_pages", method: "GET", path: "/library/wiki", status: http.StatusOK},
	{module: module.Media, tool: "media_downloads", table: "media_channels", method: "GET", path: "/apps/media/channels", status: http.StatusOK,
		command: []string{"media", "poll"}, output: nonEmpty},
	{module: module.Assistant, tool: "kb_search", table: "threads", method: "GET", path: "/library/files", status: http.StatusOK,
		command: []string{"agent", "run", "--text", "hello", "--kind", "chat"}, output: func(stdout, _ string, code int) bool {
			return code == cli.ExitOK && strings.HasPrefix(stdout, "{")
		}},
	{module: module.Notifications, tool: "notify", table: "notifications", method: "GET", path: "/settings/notifications", status: http.StatusOK},
	{module: module.Grafana, tool: "grafana_datasources"},
	{module: module.Contacts, tool: "contacts_lookup", command: []string{"contacts", "sync"}},
}

type appRun struct {
	code           int
	stdout, stderr string
}

func runApp(t *testing.T, args ...string) appRun {
	var out bytes.Buffer
	var errs lockedBuffer
	app := newTestApp(t)
	if f := fixtureFor(t); f != nil {
		app = f.app
	}
	code := app.Run(context.Background(), args, &out, &errs)
	return appRun{code, out.String(), errs.String()}
}

func runAppLogged(t *testing.T, args ...string) appRun {
	var out bytes.Buffer
	var errs lockedBuffer
	app := newTestApp(t)
	if f := fixtureFor(t); f != nil {
		app = f.app
	}
	app.SetLogSetup(logx.Setup)
	defer app.SetLogSetup(quietLog)
	code := app.Run(context.Background(), args, &out, &errs)
	return appRun{code, out.String(), errs.String()}
}

type modelLog struct {
	*anthropictest.Server
	mu   sync.Mutex
	seen []anthropictest.Request
}

func (m *modelLog) Requests() []anthropictest.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.seen)
}

func moduleFixture(t *testing.T, flags map[string]string) (dir string, api *modelLog, f *fixture) {
	return moduleFixtureAt(t, flags, nil)
}

func moduleFixtureAt(t *testing.T, flags map[string]string, now func() float64) (dir string, api *modelLog, f *fixture) {
	t.Helper()
	dir = t.TempDir()
	api = &modelLog{Server: anthropictest.New(t, anthropictest.Reply("end_turn", anthropictest.Text("hi")))}
	api.OnRequest = func(r anthropictest.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.seen = append(api.seen, r)
	}
	env := map[string]string{
		"ATRIUM_DB":          filepath.Join(dir, "atrium.db"),
		"ATRIUM_CACHE_DIR":   filepath.Join(dir, "cache"),
		"ATRIUM_PUBLIC_HOST": "",
		"ATRIUM_CONFIG":      "",
		anthropic.BaseURLEnv: api.URL,
	}
	for name, value := range moduleEnv(dir) {
		env[name] = value
	}
	if key, ok := varsFor(t).lookup("ATRIUM_DB_KEY"); ok {
		env["ATRIUM_DB_KEY"] = key
	}
	f = &fixture{t: t, dir: dir, env: env, flags: flags}
	f.app = newAppWith(f.lookup, imapclient.Opener, now, quietLog)
	f.settings, f.state = f.app.Settings, f.app.State
	fixturesByTest.Store(t, f)
	fixturesByTest.Store(testKey(t), f)
	seedModuleFlags(t, f, flags)
	f.app = newAppWith(f.lookup, imapclient.Opener, now, quietLog)
	f.settings, f.state = f.app.Settings, f.app.State
	return dir, api, f
}

func seedModuleFlags(t *testing.T, f *fixture, flags map[string]string) {
	t.Helper()
	path, _ := f.lookup("ATRIUM_DB")
	seedFlagsAt(t, f.app, path, flags)
}

func allFlagsTrue() map[string]string {
	flags := map[string]string{}
	for _, name := range moduleNames {
		flags[name] = "true"
	}
	return flags
}

func seedFlagsAt(t *testing.T, app *cli.App, path string, flags map[string]string) {
	t.Helper()
	var out, errs bytes.Buffer
	if code := app.Run(context.Background(), []string{"settings", "list"}, &out, &errs); code != cli.ExitOK {
		t.Fatalf("settings list before seeding module flags: exit %d stderr %q", code, errs.String())
	}
	kv := map[string]string{}
	for _, name := range moduleNames {
		if value, ok := flags[name]; ok {
			kv[module.FlagKey(name)] = value
		}
	}
	seedSettingsAt(t, path, kv)
}

func flagsExcept(name string, value string) map[string]string {
	flags := map[string]string{}
	for _, n := range moduleNames {
		flags[n] = "true"
	}
	flags[name] = value
	return flags
}

func offeredTools(t *testing.T, api *modelLog, ran appRun) []string {
	t.Helper()
	requests := api.Requests()
	if ran.code != cli.ExitOK || len(requests) == 0 {
		t.Fatalf("agent run: exit %d, %d model requests, stdout %q stderr %q", ran.code, len(requests), ran.stdout, ran.stderr)
	}
	var names []string
	tools, _ := requests[len(requests)-1].JSON(t)["tools"].([]any)
	for _, raw := range tools {
		if entry, ok := raw.(map[string]any); ok {
			if name, ok := entry["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

func tableExists(t *testing.T, dir, table string) bool {
	t.Helper()
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func probe(t *testing.T, base string, c moduleCase) int {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/about", nil); status != http.StatusOK {
		t.Fatalf("GET /about = %d, want the core page", status)
	}
	status, _, _ := fetch(t, client, c.method, base+c.path, c.form)
	return status
}

func TestModuleTogglesThroughTheApplication(t *testing.T) {
	t.Parallel()
	for _, c := range moduleCases {
		t.Run(c.module+"/disabled", func(t *testing.T) {
			dir, api, _ := moduleFixture(t, flagsExcept(c.module, "false"))
			if c.path != "" {
				if status := probe(t, startServeIn(t, dir), c); status != http.StatusNotFound {
					t.Errorf("%s %s = %d, want 404 while %s is off", c.method, c.path, status, c.module)
				}
			}
			if c.command != nil {
				if got := runApp(t, c.command...); got.code != cli.ExitOK || got.stdout != "" {
					t.Errorf("%v while off: exit %d stdout %q stderr %q, want a silent exit 0", c.command, got.code, got.stdout, got.stderr)
				}
			}
			if c.module != module.Assistant {
				if c.tool != "" {
					if names := offeredTools(t, api, runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")); slices.Contains(names, c.tool) {
						t.Errorf("tool %s is offered while %s is off", c.tool, c.module)
					}
				}
			} else if n := len(api.Requests()); n != 0 {
				t.Errorf("assistant is off but %d model requests were sent", n)
			}
			if c.table != "" && !tableExists(t, dir, c.table) {
				t.Errorf("table %s is missing while %s is off", c.table, c.module)
			}
		})
		t.Run(c.module+"/enabled", func(t *testing.T) {
			dir, api, _ := moduleFixture(t, flagsExcept(c.module, "true"))
			if c.path != "" {
				if status := probe(t, startServeIn(t, dir), c); status != c.status {
					t.Errorf("%s %s = %d, want %d", c.method, c.path, status, c.status)
				}
			}
			ran := appRun{}
			if c.output != nil {
				ran = runApp(t, c.command...)
				if !c.output(ran.stdout, ran.stderr, ran.code) {
					t.Errorf("%v: exit %d stdout %q stderr %q", c.command, ran.code, ran.stdout, ran.stderr)
				}
			}
			if c.module != module.Assistant {
				ran = runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")
			}
			if c.tool != "" {
				if names := offeredTools(t, api, ran); !slices.Contains(names, c.tool) {
					t.Errorf("tool %s is missing while %s is on: %v", c.tool, c.module, names)
				}
			}
			if c.table != "" && !tableExists(t, dir, c.table) {
				t.Errorf("table %s is missing", c.table)
			}
		})
	}
	t.Run("live routes and security", func(t *testing.T) {
		_, _, f := moduleFixture(t, flagsExcept(module.Media, "false"))
		base, _, _, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "proxy", "--allowed-host", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		request := func(method, path string, authenticated bool) int {
			t.Helper()
			req, err := http.NewRequest(method, base+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if authenticated {
				req.Header.Set("Remote-Groups", "atrium_access")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode
		}
		if got := request(http.MethodGet, "/apps/media/channels", true); got != http.StatusNotFound {
			t.Fatalf("disabled route = %d", got)
		}
		if got := runApp(t, "settings", "set", module.FlagKey(module.Media), "invalid"); got.code == cli.ExitOK {
			t.Fatal("invalid module flag was accepted")
		}
		if got := request(http.MethodGet, "/apps/media/channels", true); got != http.StatusNotFound {
			t.Fatalf("refused setting changed the live route = %d", got)
		}
		for _, enabled := range []string{"true", "false", "true"} {
			if got := runApp(t, "settings", "set", module.FlagKey(module.Media), enabled); got.code != cli.ExitOK {
				t.Fatalf("toggle %s: %s", enabled, got.stderr)
			}
			want := http.StatusOK
			if enabled == "false" {
				want = http.StatusNotFound
			}
			if got := request(http.MethodGet, "/apps/media/channels", true); got != want {
				t.Fatalf("live route after %s = %d, want %d", enabled, got, want)
			}
			if got := request(http.MethodGet, "/about", true); got != http.StatusOK {
				t.Fatalf("core route after %s = %d", enabled, got)
			}
		}
		if got := request(http.MethodGet, "/apps/media/channels", false); got != http.StatusForbidden {
			t.Fatalf("newly enabled route without authentication = %d", got)
		}
		if got := request(http.MethodPost, "/apps/media/channels", true); got != http.StatusForbidden {
			t.Fatalf("newly enabled route without CSRF = %d", got)
		}
	})
}

func TestDisabledModuleKeepsPageAndAPIErrorResponses(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept(module.Assistant, "false"))
	base := startServeIn(t, dir)
	status, body, header := fetch(t, http.DefaultClient, http.MethodGet, base+"/library/files", nil)
	if status != http.StatusNotFound || !strings.HasPrefix(header.Get("Content-Type"), "text/html") || !strings.Contains(body, "Nothing here") {
		t.Fatalf("disabled module page = %d %s: %.200s", status, header.Get("Content-Type"), body)
	}
	issued := runApp(t, "auth", "token", "issue", "--name", "module-errors")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue API token: %s", issued.stderr)
	}
	site := &apiSite{t: t, base: base}
	secret := strings.TrimSpace(issued.stdout)
	expectAPI(t, site.do(http.MethodGet, "/api/v1/threads", secret, nil), http.StatusNotFound, "not_found")
}

func TestOptionalModuleEvaluationFailureIsUnavailableAndCoreFailureRefusesToServe(t *testing.T) {
	t.Run("optional", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, allFlagsTrue())
		d := mustConnect(t, filepath.Join(dir, "atrium.db"))
		result, err := d.Write.Exec("UPDATE connections SET expires_at = 'broken' WHERE id IN ('service/embedding', 'service/qbittorrent')")
		if err != nil {
			t.Fatal(err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 2 {
			t.Fatalf("corrupt connection expiry: rows %d, error %v", affected, err)
		}
		base, logs := startServeLogged(t, dir)
		waitFor(t, func() bool { return strings.Count(logs.String(), "module is unavailable") == 2 })
		for _, name := range []string{"mail", "media"} {
			if text := logs.String(); !strings.Contains(text, "MODULE="+name+" STATE=unavailable REASON=") || !strings.Contains(text, "expires_at") {
				t.Fatalf("the unavailable %s module and its reason are not logged: %s", name, text)
			}
		}
		for path, want := range map[string]int{"/triage": http.StatusNotFound, "/apps/media/channels": http.StatusNotFound, "/chat": http.StatusOK, "/about": http.StatusOK} {
			if status, _, _ := fetch(t, http.DefaultClient, http.MethodGet, base+path, nil); status != want {
				t.Fatalf("GET %s = %d, want %d", path, status, want)
			}
		}
		status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/settings/media", nil)
		if status != http.StatusOK || !strings.Contains(body, "Unavailable") || !strings.Contains(body, "data-unavailable") || !strings.Contains(body, "expires_at") || strings.Contains(body, "data-restart") {
			t.Fatalf("media settings page = %d, want the unavailable state and its reason: %s", status, body)
		}
	})
	t.Run("core", func(t *testing.T) {
		dir, _, f := moduleFixture(t, allFlagsTrue())
		d := mustConnect(t, filepath.Join(dir, "atrium.db"))
		for _, q := range []string{
			"DROP TRIGGER settings_audit_no_delete",
			"DELETE FROM settings_audit WHERE key = 'modules_seeded'",
			"CREATE TRIGGER refuse_module_seed BEFORE INSERT ON settings_audit WHEN NEW.key = 'modules_seeded' BEGIN SELECT RAISE(ABORT, 'module seed refused'); END",
		} {
			if _, err := d.Write.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		f.app.SetLogSetup(logx.Setup)
		defer f.app.SetLogSetup(quietLog)
		base, logs, stop, err := startServeAppOnce(t, f.app, &lockedBuffer{})
		if stop != nil {
			stop()
		}
		if err == nil || base != "" {
			t.Fatalf("a core evaluation failure served at %q instead of exiting: %s", base, logs.String())
		}
		if !strings.Contains(err.Error(), "serve exited with 1") || !strings.Contains(logs.String(), "module: core evaluation:") || !strings.Contains(logs.String(), "module seed refused") {
			t.Fatalf("startup failure does not name core and the cause: %v; %s", err, logs.String())
		}
	})
}

func TestBrokenGrafanaConnectionStaysRepairable(t *testing.T) {
	dir, api, _ := moduleFixture(t, allFlagsTrue())
	ca := filepath.Join(dir, "grafana-ca.pem")
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigEdit(t, dir, "host-grafana-ca.toml", map[string]string{
		`capabilities = ["viewer"]`: `capabilities = ["viewer"]` + "\ntransport = { ca_file = " + strconv.Quote(ca) + " }",
	}))
	set := runAppLogged(t, "settings", "set", "monthly_budget_usd", "150")
	if set.code != cli.ExitOK {
		t.Fatalf("unrelated setting with a broken Grafana CA: exit %d stderr %q", set.code, set.stderr)
	}
	if strings.Count(set.stderr, "module is unavailable") != 1 || !strings.Contains(set.stderr, "MODULE=grafana") || !strings.Contains(set.stderr, "grafana-ca.pem cannot be read") {
		t.Fatalf("the unavailable module is not logged once with its reason: %s", set.stderr)
	}
	for _, args := range [][]string{
		{"connections", "list", "--json"},
		{"secrets", "status"},
		{"db", "check"},
	} {
		if got := runApp(t, args...); got.code != cli.ExitOK {
			t.Fatalf("%v with a broken Grafana CA: exit %d stderr %q", args, got.code, got.stderr)
		}
	}
	base := startServeIn(t, dir)
	w := asstBrowse(t, base)
	d := asstDB(t, dir)
	offered := func() bool {
		t.Helper()
		start := len(api.Requests())
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		thread := w.start(map[string]string{"text": "check grafana"})
		if state, detail := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
			t.Fatalf("chat run: %s %s", state, detail)
		}
		requests := api.Requests()
		if len(requests) <= start {
			t.Fatal("chat run made no model request")
		}
		tools, _ := requests[len(requests)-1].JSON(t)["tools"].([]any)
		for _, raw := range tools {
			if entry, ok := raw.(map[string]any); ok && entry["name"] == "grafana_datasources" {
				return true
			}
		}
		return false
	}
	if offered() {
		t.Fatal("grafana tools are offered while the Grafana connection is broken")
	}
	if got := runApp(t, "settings", "set", module.FlagKey(module.Grafana), "false"); got.code != cli.ExitOK {
		t.Fatalf("disable the broken module: exit %d stderr %q", got.code, got.stderr)
	}
	if got := runApp(t, "settings", "set", module.FlagKey(module.Grafana), "true"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "grafana-ca.pem cannot be read") {
		t.Fatalf("enabling the broken module: exit %d stderr %q, want a refusal naming the CA file", got.code, got.stderr)
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	cert := srv.Certificate().Raw
	srv.Close()
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "settings", "set", module.FlagKey(module.Grafana), "true"); got.code != cli.ExitOK {
		t.Fatalf("enable the repaired module: exit %d stderr %q", got.code, got.stderr)
	}
	if !offered() {
		t.Fatal("grafana tools are not offered after the repair")
	}
}

func TestDisabledModuleRoutesEndInNotFound(t *testing.T) {
	t.Parallel()
	flags := allFlagsTrue()
	flags[module.Media], flags[module.Notifications] = "false", "false"
	dir, _, _ := moduleFixture(t, flags)
	base := startServeIn(t, dir)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/media", "/media/", "/media/channels", "/media/file/1/poster.jpg"} {
		status, body, header := fetch(t, client, http.MethodGet, base+path, nil)
		if status != http.StatusNotFound || header.Get("Location") != "" || !strings.Contains(body, "Nothing here") {
			t.Errorf("GET %s with media off = %d Location %q, want a terminal 404 page", path, status, header.Get("Location"))
		}
	}
	issued := runApp(t, "auth", "token", "issue", "--name", "disabled-routes")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue API token: %s", issued.stderr)
	}
	site := &apiSite{t: t, base: base}
	secret := strings.TrimSpace(issued.stdout)
	for _, path := range []string{"/api/v1/inbox", "/api/v1/inbox/"} {
		expectAPI(t, site.do(http.MethodGet, path, secret, nil), http.StatusNotFound, "not_found")
	}
}

func TestDisabledAssistantDoesNoLifecycleWork(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept(module.Assistant, "false"))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, created_at, updated_at) VALUES ('chat', 'Lifecycle thread', 'low', ?, ?)", now, now); err != nil {
		t.Fatal(err)
	}
	jobs := func() int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	base := startServeIn(t, dir)
	about := func() string {
		t.Helper()
		status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/about", nil)
		if status != http.StatusOK {
			t.Fatalf("GET /about = %d", status)
		}
		return body
	}
	toggle := func(value string) {
		t.Helper()
		if got := runApp(t, "settings", "set", module.FlagKey(module.Assistant), value); got.code != cli.ExitOK {
			t.Fatalf("assistant_enabled %s: exit %d stderr %q", value, got.code, got.stderr)
		}
	}
	if body := about(); strings.Contains(body, "Lifecycle thread") || strings.Contains(body, "sb-thread") {
		t.Fatal("/about shows the assistant thread sidebar while the assistant is off")
	}
	time.Sleep(time.Second)
	if n := api.ModelLists(); n != 0 {
		t.Fatalf("%d model catalog requests while the assistant is off", n)
	}
	if n := jobs(); n != 0 {
		t.Fatalf("%d scheduler jobs seeded while the assistant is off", n)
	}
	toggle("true")
	if n := jobs(); n == 0 {
		t.Fatal("enabling the assistant seeded no scheduler jobs")
	}
	waitFor(t, func() bool { return api.ModelLists() > 0 })
	if n := api.ModelLists(); n != 1 {
		t.Fatalf("%d model catalog requests after enabling the assistant, want 1", n)
	}
	if body := about(); !strings.Contains(body, "Lifecycle thread") {
		t.Fatal("/about has no thread sidebar after enabling the assistant")
	}
	toggle("false")
	if body := about(); strings.Contains(body, "Lifecycle thread") {
		t.Fatal("/about still shows the thread sidebar after disabling the assistant")
	}
	toggle("true")
	time.Sleep(time.Second)
	if n := api.ModelLists(); n != 1 {
		t.Fatalf("%d model catalog requests after enabling the assistant twice, want 1", n)
	}
}

func TestSettingsSavesDuringTheFirstAssistantEnableAllFinish(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept(module.Assistant, "false"))
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/auth", nil); status != http.StatusOK {
		t.Fatalf("GET /settings/auth = %d", status)
	}
	const action = "/settings/groups/core/sessions"
	save := func(n int) (int, error) {
		req, err := http.NewRequest(http.MethodPost, base+action, strings.NewReader(url.Values{"oidc_session_idle_days": {strconv.Itoa(n%7 + 1)}}.Encode()))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range jar.Cookies(req.URL) {
			if c.Name == "atrium_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	stop := make(chan struct{})
	failures := make(chan string, 8)
	started := make(chan struct{}, 8)
	var savers sync.WaitGroup
	for w := range 2 {
		savers.Go(func() {
			for i := 1; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				status, err := save(w + i)
				if err != nil || status != http.StatusSeeOther {
					failures <- fmt.Sprintf("concurrent save = %d: %v", status, err)
					return
				}
				select {
				case started <- struct{}{}:
				default:
				}
			}
		})
	}
	<-started
	<-started
	enabled := make(chan appRun, 1)
	go func() { enabled <- runApp(t, "settings", "set", module.FlagKey(module.Assistant), "true") }()
	select {
	case got := <-enabled:
		if got.code != cli.ExitOK {
			t.Fatalf("assistant_enabled true: exit %d stderr %q", got.code, got.stderr)
		}
	case <-time.After(time.Minute):
		t.Fatal("enabling the assistant did not finish while settings saves ran")
	}
	close(stop)
	done := make(chan struct{})
	go func() { savers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("concurrent settings saves did not finish after the assistant was enabled")
	}
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	if status, err := save(1); err != nil || status != http.StatusSeeOther {
		t.Fatalf("a later save = %d: %v", status, err)
	}
	if row := runSettings(t, "get", "oidc_session_idle_days").row(t); row.Value == nil || *row.Value != "2" {
		t.Errorf("oidc_session_idle_days = %+v, want 2", row)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var jobs int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&jobs); err != nil || jobs == 0 {
		t.Fatalf("%d scheduler jobs after the first enable: %v", jobs, err)
	}
}

func TestFailedAssistantStartIsRetriedAtTheNextPublication(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept(module.Assistant, "false"))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("CREATE TRIGGER jobs_refused BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'jobs refused'); END"); err != nil {
		t.Fatal(err)
	}
	base, logs := startServeLogged(t, dir)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	jobs := func() int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	set := func(key, value string) {
		t.Helper()
		if got := runApp(t, "settings", "set", key, value); got.code != cli.ExitOK {
			t.Fatalf("settings set %s %s: exit %d stderr %q", key, value, got.code, got.stderr)
		}
	}
	failures := func() int { return strings.Count(logs.String(), "module start failed") }
	set(module.FlagKey(module.Assistant), "true")
	if n := failures(); n != 1 {
		t.Fatalf("%d start failures logged after the first enable, want 1", n)
	}
	nav := func() string {
		t.Helper()
		status, page, _ := fetch(t, client, http.MethodGet, base+"/settings/auth", nil)
		if status != http.StatusOK {
			t.Fatalf("GET /settings/auth = %d", status)
		}
		_, rest, found := strings.Cut(page, `href="/settings/assistant"`)
		if !found {
			return ""
		}
		value, _, _ := strings.Cut(rest, ">")
		return "link " + strings.TrimSpace(value)
	}
	if got := nav(); got != "" {
		t.Fatalf("the settings nav offers the assistant after a failed start: %q", got)
	}
	if text := logs.String(); !strings.Contains(text, "MODULE=assistant") || !strings.Contains(text, "jobs refused") {
		t.Fatalf("the failed start and its reason are not logged: %s", text)
	}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/library/files", nil); status != http.StatusNotFound {
		t.Fatalf("GET /library/files after a failed start = %d, want 404", status)
	}
	set("thread_cost_cap_usd", "12")
	if n := failures(); n != 2 {
		t.Fatalf("%d start failures logged after the retry, want 2", n)
	}
	if n := strings.Count(logs.String(), "MODULE=assistant STATE=unavailable"); n != 0 {
		t.Fatalf("a start failure also logged %d unavailable transitions", n)
	}
	if _, err := d.Write.Exec("DROP TRIGGER jobs_refused"); err != nil {
		t.Fatal(err)
	}
	set("thread_cost_cap_usd", "13")
	if n := failures(); n != 2 {
		t.Fatalf("%d start failures logged after the repair, want 2", n)
	}
	if n := jobs(); n == 0 {
		t.Fatal("the retried start seeded no scheduler jobs")
	}
	if got := nav(); got != `link data-mark="on"` {
		t.Fatalf("assistant settings nav after the retried start = %q, want an on link", got)
	}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/library/files", nil); status != http.StatusOK {
		t.Fatalf("GET /library/files after the retried start = %d, want 200", status)
	}
	set("thread_cost_cap_usd", "14")
	if n := failures(); n != 2 {
		t.Fatalf("%d start failures logged after a later publication, want 2", n)
	}
}

func clearModuleEnv(t *testing.T) {
	t.Helper()
	for name := range requirementEnv("") {
		setEnv(t, name, "")
	}
	for _, name := range moduleNames {
		setEnv(t, flagEnv(name), "")
	}
}

func TestEnablingAModuleWithUnmetRequirementsIsRefusedWithTheReason(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	clearModuleEnv(t)
	for _, tc := range []struct{ module, reason string }{
		{module.Mail, "mail_chat_url is not set"},
		{module.Calendar, "iCloud CalDAV login or Google Calendar credentials"},
		{module.Media, "media_root is not set"},
		{module.Assistant, "the Anthropic connection has no API key"},
		{module.Contacts, "contacts_dir is not set"},
		{module.Grafana, "the Grafana connection is not configured"},
		{module.Grafana, "needs assistant"},
	} {
		got := runSettings(t, "set", module.FlagKey(tc.module), "true")
		if got.code != cli.ExitError || !strings.Contains(got.stderr, tc.module+" needs settings") || !strings.Contains(got.stderr, tc.reason) {
			t.Errorf("enabling %s: exit %d stderr %q, want a refusal naming %q", tc.module, got.code, got.stderr, tc.reason)
		}
		if v, ok := storedSetting(t, filepath.Join(dir, "atrium.db"), module.FlagKey(tc.module)); ok {
			t.Errorf("%s_enabled stored as %q after a refusal", tc.module, v)
		}
	}
	for name, value := range requirementEnv(filepath.Join(dir, "media")) {
		setEnv(t, name, value)
	}
	if got := runApp(t, "connections", "list"); got.code != cli.ExitOK {
		t.Fatalf("connections list: exit %d %q", got.code, got.stderr)
	}
	for _, name := range []string{module.Mail, module.Notifications} {
		if got := runSettings(t, "set", module.FlagKey(name), "true"); got.code != cli.ExitOK || got.row(t).Value == nil || *got.row(t).Value != "true" {
			t.Errorf("enabling %s with its settings met: exit %d stdout %q stderr %q", name, got.code, got.stdout, got.stderr)
		}
	}
	if got := runSettings(t, "set", module.FlagKey(module.Grafana), "true"); got.code != cli.ExitError || !strings.Contains(got.stderr, "needs assistant (off)") {
		t.Errorf("enabling grafana while assistant is off: exit %d stderr %q", got.code, got.stderr)
	}
	if got := runSettings(t, "set", module.FlagKey(module.Assistant), "true"); got.code != cli.ExitOK {
		t.Fatalf("enabling assistant: exit %d %q", got.code, got.stderr)
	}
	if got := runSettings(t, "set", module.FlagKey(module.Grafana), "true"); got.code != cli.ExitOK {
		t.Errorf("enabling grafana with assistant on: exit %d %q", got.code, got.stderr)
	}
}

func TestSettingsListWorksFromTheRootCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	clearModuleEnv(t)
	got := runApp(t, "settings", "list")
	var rows []settingRow
	if err := json.Unmarshal([]byte(got.stdout), &rows); err != nil || got.code != cli.ExitOK {
		t.Fatalf("settings list: exit %d stderr %q: %v", got.code, got.stderr, err)
	}
	for _, name := range moduleNames {
		if !slices.ContainsFunc(rows, func(r settingRow) bool { return r.Key == module.FlagKey(name) }) {
			t.Errorf("settings list has no %s", module.FlagKey(name))
		}
	}
}

func readFlag(t *testing.T, name string) string {
	t.Helper()
	got := runSettings(t, "get", module.FlagKey(name))
	row := got.row(t)
	if got.code != cli.ExitOK || row.Value == nil {
		t.Fatalf("get %s: exit %d stdout %q stderr %q", module.FlagKey(name), got.code, got.stdout, got.stderr)
	}
	return *row.Value
}

func TestFirstStartAdoptsAPhaseADatabaseAndKeepsAnEmptyOneToCore(t *testing.T) {
	t.Parallel()
	phaseA := func(t *testing.T, populated bool) (base string, path string) {
		dir := t.TempDir()
		path = filepath.Join(dir, "atrium.db")
		setEnv(t, "ATRIUM_DB", path)
		setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
		setEnv(t, "ATRIUM_PUBLIC_HOST", "")
		setEnv(t, "ATRIUM_CONFIG", "")
		clearModuleEnv(t)
		for name, value := range requirementEnv(filepath.Join(dir, "media")) {
			setEnv(t, name, value)
		}
		if got := runSettings(t, "list"); got.code != cli.ExitOK {
			t.Fatalf("settings list: exit %d %q", got.code, got.stderr)
		}
		if populated {
			d, err := db.Connect(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				"INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')",
				"INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)",
			} {
				if _, err := d.Write.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return startServeIn(t, dir), path
	}

	t.Run("a Phase A database turns every module on", func(t *testing.T) {
		base, _ := phaseA(t, true)
		for _, c := range moduleCases {
			if got := readFlag(t, c.module); got != "true" {
				t.Errorf("%s_enabled = %s after adoption, want true", c.module, got)
			}
			if c.path != "" {
				if status := probe(t, base, c); status != c.status {
					t.Errorf("%s %s = %d after adoption, want %d", c.method, c.path, status, c.status)
				}
			}
		}
	})

	t.Run("an empty database starts core only", func(t *testing.T) {
		base, _ := phaseA(t, false)
		for _, c := range moduleCases {
			if got := readFlag(t, c.module); got != "false" {
				t.Errorf("%s_enabled = %s on an empty database, want false", c.module, got)
			}
			if c.path != "" {
				if status := probe(t, base, c); status != http.StatusNotFound {
					t.Errorf("%s %s = %d on an empty database, want 404", c.method, c.path, status)
				}
			}
		}
		if status, _, _ := fetch(t, http.DefaultClient, "GET", base+"/review", nil); status != http.StatusOK {
			t.Errorf("GET /review = %d, want the core review page", status)
		}
	})
}

func TestMailPassScansEventCandidatesOnlyWhileCalendarIsOn(t *testing.T) {
	t.Parallel()
	data := loadGoldenData(t)
	candidates := func(flag string) int {
		f := newFixture(t, data, nil, "")
		seedFlagsAt(t, f.app, filepath.Join(f.dir, "atrium.db"), map[string]string{module.Calendar: flag})
		seed, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			"DELETE FROM event_candidates",
			"UPDATE messages SET seen = 0, sent = 0, in_inbox = 1, gone_at = NULL WHERE account = 'icloud' AND id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 3)",
		} {
			if _, err := seed.Write.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if code, _, stderr := f.Run(ctx, "watch", "--account", "icloud"); code != cli.ExitOK {
			t.Fatalf("watch with calendar %s: exit %d %q", flag, code, stderr)
		}
		d, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Close() }()
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM event_candidates").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if off, on := candidates("false"), candidates("true"); off != 0 || on == 0 {
		t.Errorf("event candidates after a mail pass: %d with calendar off (want none), %d with it on (want more)", off, on)
	}
}

func TestMailPauseUnavailableWhileMailIsOff(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, "false"))
	paused := func() bool {
		got := runApp(t, "live", "status")
		return got.code == cli.ExitOK && strings.Contains(got.stdout, `"paused": true`)
	}
	if got := runApp(t, "live", "pause"); got.code != cli.ExitOK || !paused() {
		t.Fatalf("live pause while mail is off: exit %d stderr %q, paused %v", got.code, got.stderr, paused())
	}
	if got := runApp(t, "live", "resume"); got.code != cli.ExitOK || paused() {
		t.Fatalf("live resume while mail is off: exit %d stderr %q", got.code, got.stderr)
	}
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	fetch(t, client, "GET", base+"/about", nil)
	for _, path := range []string{"/live/pause", "/live/resume"} {
		if status, _, _ := fetch(t, client, "POST", base+path, url.Values{}); status != http.StatusNotFound {
			t.Errorf("POST %s while mail is off = %d, want 404", path, status)
		}
	}
}

func TestMailDisableDuringProposalApproval(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Bills", mailtest.NewBox(3))
	h, x := mailtest.RawWith("Bank statement", "statements@bank.test", "disable-approval", "Your statement is ready.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	m := mailEffectsFixture(t, "live", "icloud", fake)
	m.ruleFor(t, "work", "Bills", "statements@bank.test")
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	m.run(t, "live", "backlog")
	proposal := m.scalar(t, "SELECT CAST(id AS TEXT) FROM proposals WHERE kind = 'inbox_backlog' AND status = 'pending' ORDER BY id DESC LIMIT 1")
	startServeIn(t, m.dir)
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var arrivedOnce sync.Once
	fake.StatusHook = func(_ string, count uint32) uint32 {
		arrivedOnce.Do(func() {
			close(arrived)
			<-release
		})
		return count
	}
	result := make(chan appRun, 1)
	go func() { result <- asstRunApp(m.app, context.Background(), "proposals", "approve", proposal) }()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("approval did not reach IMAP preflight")
	}
	asstSet(t, module.FlagKey(module.Mail), "false")
	asstSet(t, module.FlagKey(module.Mail), "true")
	once.Do(func() { close(release) })
	ran := <-result
	if ran.code == cli.ExitOK || !strings.Contains(ran.stderr+ran.stdout, "the mail module was disabled; start the action again") {
		t.Fatalf("approval after disable and re-enable: %d %q %q", ran.code, ran.stdout, ran.stderr)
	}
	fake.StatusHook = nil
	if len(fake.Writes) != 0 || m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'proposal'") != 0 {
		t.Fatalf("disabled approval wrote IMAP %v or recorded a proposal effect", m.verbs())
	}
	m.run(t, "proposals", "approve", proposal)
	if len(fake.Boxes["Bills"].Box.Messages) != 1 {
		t.Fatal("approval after re-enabling did not move the message")
	}
}

type midBatch struct {
	m        *mailSafety
	fake     *mailtest.FakeClient
	proposal string
}

func midBatchFixture(t *testing.T) midBatch {
	t.Helper()
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Bills", mailtest.NewBox(3))
	fake.AddBox("Receipts", mailtest.NewBox(4))
	for _, msg := range [][3]string{
		{"Bank statement", "statements@bank.test", "mid-batch-1"},
		{"Shop receipt", "orders@shop.test", "mid-batch-2"},
	} {
		h, x := mailtest.RawWith(msg[0], msg[1], msg[2], "Body.")
		inbox.Add(h, x, []string{`\Seen`}, nil)
	}
	m := mailEffectsFixture(t, "live", "icloud", fake)
	rules := filepath.Join(m.dir, "rules.json")
	if err := os.WriteFile(rules, []byte(`{"rules":[`+
		`{"id":"bank","account":"work","action":"file","dest":"Bills","when":[{"field":"from_addr","op":"eq","value":"statements@bank.test"}]},`+
		`{"id":"shop","account":"work","action":"file","dest":"Receipts","when":[{"field":"from_addr","op":"eq","value":"orders@shop.test"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.run(t, "rules", "import", rules)
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	m.run(t, "live", "backlog")
	proposal := m.scalar(t, "SELECT CAST(id AS TEXT) FROM proposals WHERE kind = 'inbox_backlog' AND status = 'pending' ORDER BY id DESC LIMIT 1")
	return midBatch{m: m, fake: fake, proposal: proposal}
}

func (b midBatch) moved() int {
	return len(b.fake.Boxes["Bills"].Box.Messages) + len(b.fake.Boxes["Receipts"].Box.Messages)
}

func (b midBatch) disableAfterWrite(t *testing.T, what string, run func()) {
	t.Helper()
	after := len(b.fake.Writes)
	arrived := make(chan struct{})
	release := make(chan struct{})
	var arrivedOnce, releaseOnce sync.Once
	free := func() { releaseOnce.Do(func() { close(release) }) }
	defer free()
	b.fake.StatusHook = func(_ string, count uint32) uint32 {
		if len(b.fake.Writes) > after {
			arrivedOnce.Do(func() {
				close(arrived)
				<-release
			})
		}
		return count
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not reach an IMAP preflight after its first write", what)
	}
	asstSet(t, module.FlagKey(module.Mail), "false")
	free()
	<-done
	b.fake.StatusHook = nil
}

func TestMailDisableMidBatchShowsWhatWasDone(t *testing.T) {
	t.Run("approve", func(t *testing.T) {
		b := midBatchFixture(t)
		var ran appRun
		b.disableAfterWrite(t, "approval", func() {
			ran = asstRunApp(b.m.app, context.Background(), "proposals", "approve", b.proposal)
		})
		if ran.code == cli.ExitOK || !strings.Contains(ran.stderr, "the mail module was disabled; start the action again") {
			t.Fatalf("approval disabled mid-batch: %d %q %q", ran.code, ran.stdout, ran.stderr)
		}
		var shown map[string]any
		if err := json.Unmarshal([]byte(ran.stdout), &shown); err != nil || shown["status"] != "failed" || shown["undoable"] != true {
			t.Fatalf("approval disabled mid-batch printed %q (%v), want the failed proposal with its undo", ran.stdout, err)
		}
		if n := b.moved(); n != 1 {
			t.Fatalf("%d of 2 messages moved, want the first group only", n)
		}
	})
	t.Run("undo", func(t *testing.T) {
		b := midBatchFixture(t)
		b.m.run(t, "proposals", "approve", b.proposal)
		if n := b.moved(); n != 2 {
			t.Fatalf("%d of 2 messages moved by the approval", n)
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		base := startServeIn(t, b.m.dir)
		fetch(t, client, http.MethodGet, base+"/about", nil)
		var status int
		var body string
		b.disableAfterWrite(t, "undo", func() {
			status, body = hxFetch(t, client, http.MethodPost, base+"/proposals/"+b.proposal+"/undo", url.Values{})
		})
		if status != http.StatusOK || !strings.Contains(body, "Undo stopped partway") || !strings.Contains(body, "Writes put back before it stopped: 1.") {
			t.Fatalf("undo disabled mid-batch = %d: %s", status, body)
		}
		if n := b.moved(); n != 1 {
			t.Fatalf("%d messages still filed after a partial undo, want 1", n)
		}
	})
	t.Run("api approve", func(t *testing.T) {
		b := midBatchFixture(t)
		s := startAPISite(t, b.m.dir, nil)
		var reply apiReply
		b.disableAfterWrite(t, "approval", func() {
			reply = s.do(http.MethodPost, "/api/v1/approvals/"+b.proposal+"/approve", s.token("owner"), nil)
		})
		approval, _ := reply.json["approval"].(map[string]any)
		problem, _ := reply.json["error"].(map[string]any)
		if reply.status != http.StatusConflict || problem["code"] != "not_approved" || reply.json["outcome"] != "failed" || approval["status"] != "failed" {
			t.Fatalf("approval disabled mid-batch = %d: %s", reply.status, reply.body)
		}
	})
	t.Run("api undo", func(t *testing.T) {
		b := midBatchFixture(t)
		b.m.run(t, "proposals", "approve", b.proposal)
		s := startAPISite(t, b.m.dir, nil)
		var reply apiReply
		b.disableAfterWrite(t, "undo", func() {
			reply = s.do(http.MethodPost, "/api/v1/undo/proposal:"+b.proposal, s.token("owner"), nil)
		})
		outcomes, _ := reply.json["outcomes"].([]any)
		problem, _ := reply.json["error"].(map[string]any)
		if reply.status != http.StatusConflict || problem["code"] != "not_undone" || len(outcomes) != 1 || reply.json["entry"] == nil {
			t.Fatalf("undo disabled mid-batch = %d: %s", reply.status, reply.body)
		}
	})
}

func TestMailUndoRefusedWhileDisabledAndHistoryRemains(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	uid := m.add("INBOX", "Bank statement", "statements@bank.test", "disable-undo", "Your statement is ready.")
	m.run(t, "sync")
	id := m.messageID(t, "Bank statement")
	m.run(t, "live", "rebaseline")
	m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')", id, mail.WallClock()+60)
	m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
	m.run(t, "sync")
	effect := m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'")
	base := startServeIn(t, m.dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/activity", nil)
	if status != http.StatusOK || !strings.Contains(body, "/activity/mail/"+effect+"/undo") {
		t.Fatalf("activity before disabling: %d %s", status, body)
	}
	writes := len(m.fake.Writes)
	arrived := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var arrivedOnce sync.Once
	m.fake.StatusHook = func(_ string, count uint32) uint32 {
		arrivedOnce.Do(func() {
			close(arrived)
			<-release
		})
		return count
	}
	type response struct {
		status int
		body   string
	}
	result := make(chan response, 1)
	go func() {
		status, body, _ := fetch(t, client, http.MethodPost, base+"/activity/mail/"+effect+"/undo", url.Values{})
		result <- response{status: status, body: body}
	}()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("undo did not reach IMAP preflight")
	}
	asstSet(t, module.FlagKey(module.Mail), "false")
	releaseOnce.Do(func() { close(release) })
	refused := <-result
	m.fake.StatusHook = nil
	if !strings.Contains(refused.body, "the mail module was disabled; start the action again") {
		t.Fatalf("disabled pending undo response %d: %s", refused.status, refused.body)
	}
	status, body, _ = fetch(t, client, http.MethodGet, base+"/activity", nil)
	if status != http.StatusOK || !strings.Contains(body, "/activity/mail/"+effect+"/undo") {
		t.Fatalf("activity while disabled: %d %s", status, body)
	}
	listed := runApp(t, "effects", "list", "--operation", "correct")
	if listed.code != cli.ExitOK || !strings.Contains(listed.stdout, `"id": `+effect) {
		t.Fatalf("historical effect missing while disabled: %d %s %s", listed.code, listed.stdout, listed.stderr)
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+"/activity/mail/"+effect+"/undo", url.Values{})
	if !strings.Contains(body, "the mail module was disabled; start the action again") {
		t.Fatalf("disabled undo response %d: %s", status, body)
	}
	if len(m.fake.Writes) != writes || m.count(t, "SELECT COUNT(*) FROM effects WHERE undo_of = ?", effect) != 0 {
		t.Fatal("disabled undo wrote IMAP or recorded an undo intent")
	}
	asstSet(t, module.FlagKey(module.Mail), "true")
	m.run(t, "live", "undo", effect)
	if len(m.fake.Boxes["INBOX"].Box.Messages) != 1 {
		t.Fatal("undo after re-enabling did not restore the message")
	}
}

func TestMailToolRefusesRunStartedBeforeDisable(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	message := seedMail(t, dir)
	startServeIn(t, dir)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("disabled-mail", "mail_read", map[string]any{"message_id": message})),
		anthropictest.Reply("end_turn", anthropictest.Text("refused")),
	)
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	previous := api.OnRequest
	api.OnRequest = func(r anthropictest.Request) {
		previous(r)
		once.Do(func() {
			close(arrived)
			<-release
		})
	}
	result := make(chan appRun, 1)
	go func() { result <- asstRunApp(f.app, context.Background(), "agent", "run", "--text", "read my mail") }()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("model request did not arrive")
	}
	asstSet(t, module.FlagKey(module.Mail), "false")
	releaseOnce.Do(func() { close(release) })
	ran := <-result
	api.OnRequest = previous
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
	resultBlock := findToolResult(t, api.Requests(), "disabled-mail")
	encoded, err := json.Marshal(resultBlock)
	if err != nil || !strings.Contains(string(encoded), "the mail module was disabled; start the action again") {
		t.Fatalf("disabled tool result = %s (%v)", encoded, err)
	}
	if resultBlock["is_error"] != true {
		t.Fatalf("disabled tool result is not an error: %s", encoded)
	}
	asstSet(t, module.FlagKey(module.Mail), "true")
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("enabled-mail", "mail_read", map[string]any{"message_id": message})),
		anthropictest.Reply("end_turn", anthropictest.Text("read")),
	)
	if ran := runApp(t, "agent", "run", "--text", "read my mail"); ran.code != cli.ExitOK {
		t.Fatalf("enabled agent run: %d %s", ran.code, ran.stderr)
	}
	enabled := findToolResult(t, api.Requests(), "enabled-mail")
	if enabled == nil || enabled["is_error"] == true {
		t.Fatalf("enabled tool result = %v", enabled)
	}
}
