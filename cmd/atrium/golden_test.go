package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/calendar"
	caldav "github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/testutil/golden"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type cliGolden struct {
	Name     string            `json:"name"`
	Argv     []string          `json:"argv"`
	ExitCode int               `json:"exit_code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
	Env      map[string]string `json:"env"`
	Stdin    string            `json:"stdin"`
	Files    map[string]string `json:"files"`
}

type worldMessage struct {
	UID    uint32   `json:"uid"`
	Flags  []string `json:"flags"`
	Labels []string `json:"labels"`
	Header string   `json:"header"`
	Text   string   `json:"text"`
	ModSeq uint64   `json:"modseq"`
}

type worldAccount struct {
	Gmail      bool                      `json:"gmail"`
	Mailboxes  map[string][]worldMessage `json:"mailboxes"`
	ModSeq     map[string]uint64         `json:"modseq"`
	NextUID    map[string]uint32         `json:"next_uid"`
	Order      []string                  `json:"order"`
	SpecialUse map[string][]string       `json:"special_use"`
	Validity   uint32                    `json:"uidvalidity"`
}

type goldenWorld struct {
	Clock      float64                 `json:"clock"`
	Accounts   map[string]worldAccount `json:"accounts"`
	Categories map[string][]uint32     `json:"gmail_categories"`
}

func loadWorld(t *testing.T) goldenWorld {
	t.Helper()
	var w goldenWorld
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "world.json"), &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w goldenWorld) fakes() map[string]*mailtest.FakeClient {
	out := map[string]*mailtest.FakeClient{}
	for name, spec := range w.Accounts {
		fake := mailtest.NewFakeClient()
		fake.Gmail = spec.Gmail
		for _, box := range spec.Order {
			fb := &mailtest.FakeBox{Validity: spec.Validity, ModSeq: spec.ModSeq[box], NextUID: spec.NextUID[box], Messages: map[uint32]*mailtest.FakeMessage{}}
			for _, m := range spec.Mailboxes[box] {
				fb.Messages[m.UID] = &mailtest.FakeMessage{Header: []byte(m.Header), Text: []byte(m.Text), Flags: m.Flags, Labels: m.Labels, ModSeq: m.ModSeq}
			}
			fake.AddBox(box, fb, spec.SpecialUse[box]...)
		}
		if spec.Gmail {
			fake.CategoryUIDs = w.Categories
		}
		out[name] = fake
	}
	return out
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o777)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o666)
	})
	if err != nil {
		t.Fatal(err)
	}
}

var pythonOrder = []string{
	"sync", "watch", "rules", "shadow-report", "reindex", "rebalance", "deletions", "gmail-labels", "live", "proposals",
	"notion", "agent", "calendar", "loops", "watches", "media", "serve", "db",
}

func inPythonOrder(have []*cli.Command) []*cli.Command {
	var out []*cli.Command
	for _, name := range pythonOrder {
		if i := slices.IndexFunc(have, func(c *cli.Command) bool { return c.Name == name }); i >= 0 {
			out = append(out, have[i])
		}
	}
	return out
}

var volatileRunFields = []string{"started_at", "ended_at", "lease_until", "owner"}

func normalizeRun(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		for _, field := range volatileRunFields {
			if strings.HasPrefix(strings.TrimLeft(line, " "), `"`+field+`": `) {
				lines[i] = `"` + field + `": <volatile>`
			}
		}
	}
	return strings.Join(lines, "\n")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

type goldenData struct {
	world       goldenWorld
	environment map[string]string
}

func loadGoldenData(t *testing.T) goldenData {
	t.Helper()
	var scenario struct {
		Environment map[string]string `json:"environment"`
	}
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "scenario.json"), &scenario); err != nil {
		t.Fatal(err)
	}
	return goldenData{world: loadWorld(t), environment: scenario.Environment}
}

type rig struct {
	dir string
	app *cli.App
}

func newRig(t *testing.T, data goldenData, extraEnv map[string]string, stdin string) *rig {
	t.Helper()
	dir := t.TempDir()
	copyTree(t, filepath.Join(golden.Dir(), "state", "cache"), filepath.Join(dir, "cache"))
	srv := llmstub.Server(t, nil)
	t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{CalDAV: caldav.New(t, data.environment["ICLOUD_APPLE_ID"], data.environment["ICLOUD_APP_PASSWORD"]).URL + "/"}))
	vars := map[string]string{}
	for k, v := range data.environment {
		vars[k] = v
	}
	vars["ATRIUM_CALENDAR_ICLOUD_APPLE_ID"] = vars["ICLOUD_APPLE_ID"]
	vars["ATRIUM_DB"] = filepath.Join(dir, "atrium.db")
	vars["ATRIUM_CACHE_DIR"] = filepath.Join(dir, "cache")
	vars["ATRIUM_CONTACTS_DIR"] = filepath.Join(golden.Dir(), "state", "contacts")
	vars["ATRIUM_MEDIA_ROOT"] = filepath.Join(dir, "media")
	vars["ATRIUM_CHAT_URL"] = srv.URL + "/v1"
	vars["ATRIUM_EMBED_URL"] = srv.URL + "/v1"
	for k, v := range extraEnv {
		vars[k] = v
	}
	for phaseA, canonical := range map[string]string{
		"ATRIUM_MODE": "ATRIUM_MAIL_MAIL_MODE", "ATRIUM_CHAT_URL": "ATRIUM_MAIL_MAIL_CHAT_URL", "ATRIUM_EMBED_URL": "ATRIUM_MAIL_MAIL_EMBED_URL",
		"ATRIUM_MEDIA_ROOT": "ATRIUM_MEDIA_MEDIA_ROOT", "QBITTORRENT_URL": "ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL",
		"ATRIUM_MEDIA_MOVIES_URL": "ATRIUM_MEDIA_MEDIA_MOVIES_URL", "ATRIUM_MEDIA_TV_URL": "ATRIUM_MEDIA_MEDIA_TV_URL",
		"ATRIUM_CONTACTS_DIR": "ATRIUM_CONTACTS_CONTACTS_DIR", "NOTION_TOKEN": "ATRIUM_NOTION_NOTION_TOKEN",
	} {
		if v, ok := vars[phaseA]; ok {
			vars[canonical] = v
		}
		delete(vars, phaseA)
	}
	accounts := []mailAccount{
		{"icloud", "icloud", vars["ICLOUD_MAIL_USERNAME"], vars["ICLOUD_APP_PASSWORD"], false},
		{"gmail", "gmail", vars["GMAIL_ADDRESS"], vars["GMAIL_APP_PASSWORD"], false},
	}
	for i, a := range accounts {
		if a.login == "" || a.password == "" {
			accounts[i] = mailAccount{a.id, a.preset, a.id, "unset", true}
		}
	}
	vars[settingEnv(config.KeyMailAccountsFile)] = accountsFile(t, dir, accounts...)
	for _, name := range []string{"ATRIUM_TZ", "ATRIUM_PUBLIC_URL", "ICLOUD_APPLE_ID", "ICLOUD_MAIL_USERNAME", "GMAIL_ADDRESS", "GMAIL_APP_PASSWORD"} {
		delete(vars, name)
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
	pins, err := config.LoadPins(config.OS, settings.Default)
	if err != nil {
		t.Fatal(err)
	}
	settings.Default.SetPins(pins)
	t.Cleanup(func() { settings.Default.SetPins(nil) })
	fakes := data.world.fakes()
	clients := func(config.Config) mail.OpenFunc {
		return func(_ context.Context, account string) (mail.Client, error) { return fakes[account], nil }
	}
	app := cli.New("atrium", logx.Setup)
	app.Add(inPythonOrder(commands(clients, func() float64 { return data.world.Clock }))...)
	app.Stdin = strings.NewReader(stdin)
	r := &rig{dir: dir, app: app}
	code, _, stderr := r.run(context.Background(), "db", "import-v1", filepath.Join(golden.Dir(), "atrium.db"))
	if code != cli.ExitOK {
		t.Fatalf("import-v1 into the fresh world database: exit %d stderr %q", code, stderr)
	}
	r.seedWorld(t, filepath.Join(golden.Dir(), "atrium.db"))
	code, _, stderr = r.run(context.Background(), "reindex")
	if code != cli.ExitOK {
		t.Fatalf("embed the seeded world: exit %d stderr %q", code, stderr)
	}
	return r
}

func (r *rig) run(ctx context.Context, argv ...string) (code int, stdout, stderr string) {
	var out, errs bytes.Buffer
	code = r.app.Run(ctx, argv, &out, &errs)
	return code, out.String(), errs.String()
}

func TestCLIGoldens(t *testing.T) {
	var index []cliGolden
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	data := loadGoldenData(t)
	for _, g := range index {
		t.Run(g.Name, func(t *testing.T) {
			r := newRig(t, data, goldenEnv(t, g), g.Stdin)
			tmp := filepath.Join(r.dir, "tmp")
			if err := os.MkdirAll(tmp, 0o777); err != nil {
				t.Fatal(err)
			}
			for name, content := range g.Files {
				if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o666); err != nil {
					t.Fatal(err)
				}
			}
			argv := make([]string, len(g.Argv)-1)
			for i, arg := range g.Argv[1:] {
				argv[i] = strings.ReplaceAll(arg, "{tmp}", tmp)
			}
			limit := 60 * time.Second
			if len(argv) > 0 && argv[0] == "watch" {
				limit = 5 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			code, stdout, stderr := r.run(ctx, argv...)
			if code != g.ExitCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, g.ExitCode, stderr)
			}
			want := string(golden.ReadFile(t, "cli", g.Stdout))
			wantErr := string(golden.ReadFile(t, "cli", g.Stderr))
			if g.Argv[1] == "agent" {
				stdout, want = normalizeRun(stdout), normalizeRun(want)
			}
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if g.ExitCode != 0 && lastLine(stderr) != lastLine(wantErr) {
				t.Errorf("last stderr line = %q, want %q", lastLine(stderr), lastLine(wantErr))
			}
		})
	}
}

func TestWritingCommandsTakeTheDatabaseLock(t *testing.T) {
	data := loadGoldenData(t)
	for _, argv := range [][]string{{"live", "rebaseline"}, {"live", "deletions"}, {"loops", "refresh"}, {"watches", "refresh"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			r := newRig(t, data, nil, "")
			lock, err := db.Exclusive(context.Background(), filepath.Join(r.dir, "atrium.db"), db.ShortLockWait)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				code, _, _ := r.run(context.Background(), argv...)
				done <- code
			}()
			select {
			case code := <-done:
				t.Fatalf("exited %d while the lock was held", code)
			case <-time.After(500 * time.Millisecond):
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != cli.ExitOK {
					t.Errorf("exit code = %d after the lock was released", code)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the command did not finish after the lock was released")
			}
		})
	}
}

var seededTables = map[string]bool{
	"schema_migrations": true, "module_migrations": true, "db_key_history": true, "secrets": true,
	"settings_audit": true, "write_resolutions": true, "pass_runs": true, "pass_run_alerts": true,
	"endpoint_probes": true, "integration_health": true, "auth_sessions": true, "auth_logins": true,
	"auth_login_links": true, "mail_accounts": true, "ntfy_channels": true, "embed_index": true,
}

var ftsTables = map[string][]string{
	"message_fts": {"subject", "sender", "body"},
	"thread_fts":  {"thread_id", "text"},
	"notion_fts":  {"title", "path", "body"},
}

func (r *rig) seedWorld(t *testing.T, source string) {
	t.Helper()
	v2, err := db.Connect(filepath.Join(r.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v2.Close() }()
	v1, err := db.OpenSource(source, db.Key{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v1.Close() }()
	columns := func(q db.Querier, table string) []string {
		rows, err := q.QueryContext(context.Background(), "SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatal(err)
		}
		names, err := db.Collect(rows, func(row *sql.Rows) (string, error) {
			var name string
			return name, row.Scan(&name)
		})
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	rows, err := v2.Read.QueryContext(context.Background(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	tables, err := db.Collect(rows, func(row *sql.Rows) (string, error) {
		var name string
		return name, row.Scan(&name)
	})
	if err != nil {
		t.Fatal(err)
	}
	var stmts []string
	for _, table := range tables {
		if seededTables[table] || table == "message_vec" || table == "notion_vec" {
			continue
		}
		if strings.HasPrefix(table, "message_fts_") || strings.HasPrefix(table, "thread_fts_") || strings.HasPrefix(table, "notion_fts_") {
			continue
		}
		v1cols := columns(v1, table)
		if len(v1cols) == 0 {
			continue
		}
		have := map[string]bool{}
		for _, c := range v1cols {
			have[c] = true
		}
		var common []string
		for _, c := range columns(v2.Read, table) {
			if have[c] {
				common = append(common, c)
			}
		}
		if len(common) == 0 {
			continue
		}
		quoted := make([]string, len(common))
		for i, c := range common {
			quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		}
		if fts, ok := ftsTables[table]; ok {
			_ = fts
			stmts = append(stmts, "INSERT INTO main."+table+" (rowid, "+strings.Join(quoted, ", ")+") SELECT rowid, "+
				strings.Join(quoted, ", ")+" FROM v1."+table+";")
			continue
		}
		stmts = append(stmts, "INSERT OR IGNORE INTO main."+table+" ("+strings.Join(quoted, ", ")+") SELECT "+
			strings.Join(quoted, ", ")+" FROM v1."+table+";")
	}
	script := "PRAGMA foreign_keys = OFF;\nATTACH DATABASE '" + strings.ReplaceAll(source, "'", "''") + "' AS v1;\n" +
		strings.Join(stmts, "\n") + "\nDETACH DATABASE v1;\n"
	saved := r.app.Stdin
	r.app.Stdin = strings.NewReader(script)
	defer func() { r.app.Stdin = saved }()
	code, _, stderr := r.run(context.Background(), "db", "shell")
	if code != cli.ExitOK {
		t.Fatalf("seed the v2 world from the v1 fixture: exit %d stderr %q", code, stderr)
	}
}

func copyGoldenFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o666); err != nil {
		t.Fatal(err)
	}
}

func goldenEnv(t *testing.T, g cliGolden) map[string]string {
	t.Helper()
	if len(g.Argv) < 2 || g.Argv[1] != "media" {
		return g.Env
	}
	return mediaServices(t)
}

func mediaServices(t *testing.T) map[string]string {
	t.Helper()
	release := func(hash, title, size string) map[string]any {
		return map[string]any{"hash": hash, "title": title, "magnet_url": "magnet:?xt=urn:btih:" + hash + "&dn=x", "size_bytes": size, "season": "1", "episode": "1"}
	}
	releases := map[string][]map[string]any{
		"1234567": {
			release(strings.Repeat("a", 40), "Lanterns S01E07 1080p WEB h264", "1500000000"),
			release(strings.Repeat("c", 40), "Lanterns S01E08 1080p WEB h264", "2500000000"),
			release(strings.Repeat("d", 40), "Lanterns S01E09 1080p WEB h264", "2600000000"),
		},
		"7654321": {},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/get-torrents", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": releases[r.URL.Query().Get("imdb_id")]})
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Ok.") })
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "[]") })
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return map[string]string{"QBITTORRENT_URL": srv.URL, "ATRIUM_MEDIA_MEDIA_EZTV_URL": srv.URL + "/api/get-torrents", "ATRIUM_NOTIFY_NTFY_URL": srv.URL}
}
