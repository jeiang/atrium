package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
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
	{module: module.Notion, tool: "search_knowledge", table: "notion_pages", method: "POST", path: "/notion/missing/link", form: url.Values{}, status: http.StatusUnprocessableEntity,
		command: []string{"notion", "wiki-plan"}, output: func(_, stderr string, code int) bool {
			return code == cli.ExitError && strings.Contains(stderr, "wiki-plan")
		}},
	{module: module.Money, tool: "spending", table: "transactions", method: "GET", path: "/money", status: http.StatusOK,
		command: []string{"money", "refresh"}, output: nonEmpty},
	{module: module.Media, tool: "media_downloads", table: "media_channels", method: "GET", path: "/media/channels", status: http.StatusOK,
		command: []string{"media", "poll"}, output: nonEmpty},
	{module: module.Assistant, tool: "profile_set", table: "threads", method: "GET", path: "/projects", status: http.StatusOK,
		command: []string{"agent", "run", "--text", "hello", "--kind", "chat"}, output: func(stdout, _ string, code int) bool {
			return code == cli.ExitOK && strings.HasPrefix(stdout, "{")
		}},
	{module: module.Notifications, tool: "notify", table: "notifications", method: "GET", path: "/settings/notifications", status: http.StatusOK},
	{module: module.Grafana, tool: "grafana_datasources"},
	{module: module.Contacts, tool: "contacts_lookup"},
}

type appRun struct {
	code           int
	stdout, stderr string
}

func runApp(args ...string) appRun {
	var out, errs bytes.Buffer
	code := newApp().Run(context.Background(), args, &out, &errs)
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

func moduleFixture(t *testing.T, flags map[string]string) (dir string, api *modelLog) {
	t.Helper()
	dir = t.TempDir()
	api = &modelLog{Server: anthropictest.New(t, anthropictest.Reply("end_turn", anthropictest.Text("hi")))}
	api.OnRequest = func(r anthropictest.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.seen = append(api.seen, r)
	}
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	t.Setenv("ATRIUM_CONFIG", "")
	t.Setenv("ATRIUM_CORE_TZ", "America/Port_of_Spain")
	for name, value := range requirementEnv(filepath.Join(dir, "media")) {
		t.Setenv(name, value)
	}
	t.Setenv("ANTHROPIC_API_KEY", anthropictest.APIKey)
	t.Setenv(anthropic.BaseURLEnv, api.URL)
	for _, name := range moduleNames {
		t.Setenv(flagEnv(name), flags[name])
	}
	return dir, api
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
	for _, c := range moduleCases {
		t.Run(c.module+"/disabled", func(t *testing.T) {
			dir, api := moduleFixture(t, flagsExcept(c.module, "false"))
			if c.path != "" {
				if status := probe(t, startServeIn(t, dir), c); status != http.StatusNotFound {
					t.Errorf("%s %s = %d, want 404 while %s is off", c.method, c.path, status, c.module)
				}
			}
			if c.command != nil {
				if got := runApp(c.command...); got.code != cli.ExitOK || got.stdout != "" {
					t.Errorf("%v while off: exit %d stdout %q stderr %q, want a silent exit 0", c.command, got.code, got.stdout, got.stderr)
				}
			}
			if c.module != module.Assistant {
				if names := offeredTools(t, api, runApp("agent", "run", "--text", "hello", "--kind", "chat")); slices.Contains(names, c.tool) {
					t.Errorf("tool %s is offered while %s is off", c.tool, c.module)
				}
			} else if n := len(api.Requests()); n != 0 {
				t.Errorf("assistant is off but %d model requests were sent", n)
			}
			if c.table != "" && !tableExists(t, dir, c.table) {
				t.Errorf("table %s is missing while %s is off", c.table, c.module)
			}
		})
		t.Run(c.module+"/enabled", func(t *testing.T) {
			dir, api := moduleFixture(t, flagsExcept(c.module, "true"))
			if c.path != "" {
				if status := probe(t, startServeIn(t, dir), c); status != c.status {
					t.Errorf("%s %s = %d, want %d", c.method, c.path, status, c.status)
				}
			}
			ran := appRun{}
			if c.output != nil {
				ran = runApp(c.command...)
				if !c.output(ran.stdout, ran.stderr, ran.code) {
					t.Errorf("%v: exit %d stdout %q stderr %q", c.command, ran.code, ran.stdout, ran.stderr)
				}
			}
			if c.module != module.Assistant {
				ran = runApp("agent", "run", "--text", "hello", "--kind", "chat")
			}
			if names := offeredTools(t, api, ran); !slices.Contains(names, c.tool) {
				t.Errorf("tool %s is missing while %s is on: %v", c.tool, c.module, names)
			}
			if c.table != "" && !tableExists(t, dir, c.table) {
				t.Errorf("table %s is missing", c.table)
			}
		})
	}
}

func clearModuleEnv(t *testing.T) {
	t.Helper()
	for name := range requirementEnv("") {
		t.Setenv(name, "")
	}
	for _, name := range moduleNames {
		t.Setenv(flagEnv(name), "")
	}
}

func TestEnablingAModuleWithUnmetRequirementsIsRefusedWithTheReason(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	clearModuleEnv(t)
	for _, tc := range []struct{ module, reason string }{
		{module.Mail, "mail_chat_url is not set"},
		{module.Calendar, "iCloud CalDAV login or Google Calendar credentials"},
		{module.Notion, "NOTION_TOKEN is not set"},
		{module.Media, "media_root is not set"},
		{module.Assistant, "anthropic_api_key is not set"},
		{module.Notifications, "ntfy_url is not set"},
		{module.Grafana, "GRAFANA_URL and GRAFANA_VIEWER_TOKEN"},
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
	if got := runSettings(t, "set", module.FlagKey(module.Money), "true"); got.code != cli.ExitOK || got.row(t).Value == nil || *got.row(t).Value != "true" {
		t.Errorf("enabling money: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	for name, value := range requirementEnv(filepath.Join(dir, "media")) {
		t.Setenv(name, value)
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
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	clearModuleEnv(t)
	got := runApp("settings", "list")
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
	phaseA := func(t *testing.T, populated bool) (base string, path string) {
		dir := t.TempDir()
		path = filepath.Join(dir, "atrium.db")
		t.Setenv("ATRIUM_DB", path)
		t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
		t.Setenv("ATRIUM_PUBLIC_HOST", "")
		t.Setenv("ATRIUM_CONFIG", "")
		clearModuleEnv(t)
		for name, value := range requirementEnv(filepath.Join(dir, "media")) {
			t.Setenv(name, value)
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
		if status, _, _ := fetch(t, http.DefaultClient, "GET", base+"/proposals", nil); status != http.StatusOK {
			t.Errorf("GET /proposals = %d, want the core review page", status)
		}
	})
}

func TestMailPassScansEventCandidatesOnlyWhileCalendarIsOn(t *testing.T) {
	data := loadGoldenData(t)
	candidates := func(flag string) int {
		r := newRig(t, data, map[string]string{flagEnv(module.Calendar): flag}, "")
		seed, err := db.Connect(filepath.Join(r.dir, "atrium.db"))
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
		if code, _, stderr := r.run(ctx, "watch", "--account", "icloud"); code != cli.ExitOK {
			t.Fatalf("watch with calendar %s: exit %d %q", flag, code, stderr)
		}
		d, err := db.Connect(filepath.Join(r.dir, "atrium.db"))
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

func TestMailPauseStaysAvailableWhileMailIsOff(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept(module.Mail, "false"))
	paused := func() bool {
		got := runApp("live", "status")
		return got.code == cli.ExitOK && strings.Contains(got.stdout, `"paused": true`)
	}
	if got := runApp("live", "pause"); got.code != cli.ExitOK || !paused() {
		t.Fatalf("live pause while mail is off: exit %d stderr %q, paused %v", got.code, got.stderr, paused())
	}
	if got := runApp("live", "resume"); got.code != cli.ExitOK || paused() {
		t.Fatalf("live resume while mail is off: exit %d stderr %q", got.code, got.stderr)
	}
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	fetch(t, client, "GET", base+"/about", nil)
	if status, _, _ := fetch(t, client, "POST", base+"/live/pause", url.Values{}); status != http.StatusSeeOther || !paused() {
		t.Errorf("POST /live/pause while mail is off = %d, paused %v", status, paused())
	}
	if status, _, _ := fetch(t, client, "POST", base+"/live/resume", url.Values{}); status != http.StatusSeeOther || paused() {
		t.Errorf("POST /live/resume while mail is off = %d, paused %v", status, paused())
	}
}
