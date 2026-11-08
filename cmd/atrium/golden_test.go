package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

type fixture struct {
	t    *testing.T
	dir  string
	env  map[string]string
	app  *cli.App
	base string
}

func newFixture(t *testing.T, data goldenData, extraEnv map[string]string, stdin string) *fixture {
	t.Helper()
	dir := t.TempDir()
	copyTree(t, filepath.Join(golden.Dir(), "state", "cache"), filepath.Join(dir, "cache"))
	copyTree(t, filepath.Join(golden.Dir(), "state", "contacts"), filepath.Join(dir, "contacts"))
	srv := llmstub.Server(t, nil)
	t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{CalDAV: caldav.New(t, data.environment["ICLOUD_APPLE_ID"], data.environment["ICLOUD_APP_PASSWORD"]).URL + "/"}))
	vars := map[string]string{}
	for k, v := range data.environment {
		vars[k] = v
	}
	vars["ATRIUM_CALENDAR_ICLOUD_APPLE_ID"] = vars["ICLOUD_APPLE_ID"]
	vars["ATRIUM_DB"] = filepath.Join(dir, "atrium.db")
	vars["ATRIUM_CACHE_DIR"] = filepath.Join(dir, "cache")
	vars["ATRIUM_CONTACTS_DIR"] = filepath.Join(dir, "contacts")
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
	app.Bootstrap = bootstrap(config.OS, settings.Default)
	app.Add(commands(config.OS, settings.Default, nil, clients, func() float64 { return data.world.Clock })...)
	app.Stdin = strings.NewReader(stdin)
	copyFile(t, filepath.Join(golden.Dir(), "world.db"), filepath.Join(dir, "atrium.db"))
	f := &fixture{t: t, dir: dir, env: vars, app: app}
	f.seedNotionVectors(t)
	if code, _, stderr := f.Run(context.Background(), "reindex"); code != cli.ExitOK {
		t.Fatalf("embed the seeded world: exit %d stderr %q", code, stderr)
	}
	return f
}

func (f *fixture) lookup(name string) (string, bool) {
	if v, ok := f.env[name]; ok {
		return v, true
	}
	return config.OS(name)
}

func (f *fixture) Run(ctx context.Context, argv ...string) (code int, stdout, stderr string) {
	var out, errs bytes.Buffer
	code = f.app.Run(ctx, argv, &out, &errs)
	return code, out.String(), errs.String()
}

func (f *fixture) Serve() string {
	if f.base == "" {
		f.base = startServeIn(f.t, f.dir)
	}
	return f.base
}

func (f *fixture) seedNotionVectors(t *testing.T) {
	t.Helper()
	var vectors []notionVector
	if err := json.Unmarshal(golden.ReadFile(t, ".", "notion_vec.json"), &vectors); err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, v := range vectors {
		if _, err := d.Write.Exec("INSERT INTO notion_vec (rowid, visibility, embedding) VALUES (?, ?, ?)", v.RowID, v.Visibility, v.Embedding); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCLIGoldens(t *testing.T) {
	var index []cliGolden
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	data := loadGoldenData(t)
	for _, g := range index {
		t.Run(g.Name, func(t *testing.T) {
			f := newFixture(t, data, goldenEnv(t, g), g.Stdin)
			tmp := filepath.Join(f.dir, "tmp")
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
				limit = 35 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			code, stdout, stderr := f.Run(ctx, argv...)
			if code != g.ExitCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, g.ExitCode, stderr)
			}
			want := string(golden.ReadFile(t, "cli", g.Stdout))
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if g.ExitCode != 0 {
				wantErr := string(golden.ReadFile(t, "cli", g.Stderr))
				if lastLine(stderr) != lastLine(wantErr) {
					t.Errorf("last stderr line = %q, want %q", lastLine(stderr), lastLine(wantErr))
				}
			}
		})
	}
}

func TestWritingCommandsTakeTheDatabaseLock(t *testing.T) {
	data := loadGoldenData(t)
	for _, argv := range [][]string{{"live", "rebaseline"}, {"live", "deletions"}, {"loops", "refresh"}, {"watches", "refresh"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			f := newFixture(t, data, nil, "")
			lock, err := db.Exclusive(context.Background(), filepath.Join(f.dir, "atrium.db"), db.ShortLockWait)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				code, _, _ := f.Run(context.Background(), argv...)
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
