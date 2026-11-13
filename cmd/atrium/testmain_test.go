package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/app"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

var testCatalog = sync.OnceValue(func() *module.Catalog {
	cat, err := app.Docs()
	if err != nil {
		panic(err)
	}
	return cat
})

func testSettings() *settings.Registry { return testCatalog().Settings() }

func testProposalContext() context.Context {
	return proposals.WithHooks(settings.With(context.Background(), testSettings()), proposals.Hooks{})
}

var moduleNames = testCatalog().Optional()

func settingEnv(key string) string {
	if d, ok := testSettings().Lookup(key); ok {
		return config.EnvName(d)
	}
	if entry, ok := settings.ImportEntryFor(key); ok {
		return "ATRIUM_" + strings.ToUpper(entry.Module) + "_" + strings.ToUpper(entry.Key)
	}
	if d, ok := testSettings().HostDefOf(key); ok {
		return "ATRIUM_" + strings.ToUpper(d.Module) + "_" + strings.ToUpper(d.Key)
	}
	panic("unregistered setting " + key)
}

func flagEnv(name string) string { return settingEnv(module.FlagKey(name)) }

func hostEnv(module, key string) string {
	return "ATRIUM_" + strings.ToUpper(module) + "_" + strings.ToUpper(key)
}

var testRoot = func() string {
	dir, err := os.MkdirTemp("", "atrium-test-")
	if err != nil {
		panic("cannot create the test root")
	}
	return dir
}()

var defaultMailPassword = func() string {
	dir := filepath.Join(testRoot, "accounts")
	secret := filepath.Join(dir, "icloud.password")
	if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(secret, []byte("dummy-icloud-password\n"), 0o600) != nil {
		panic("cannot write the default mail password")
	}
	return secret
}()

func mailConnection(id, preset, login, passwordFile string, enabled bool) string {
	return fmt.Sprintf(`
[connections."mail/%s"]
kind = "mail"
label = %q
config = { preset = %q, login = %q, auth_method = "password", enabled = %q }
capabilities = ["imap", "smtp"]
credential_files = { "mail/%s/password" = %q }
`, id, id, preset, login, fmt.Sprintf("%t", enabled), id, passwordFile)
}

const defaultWebPushConnection = `
[connections."service/webpush"]
kind = "service/webpush"
label = "Web push"
config = { subject = "mailto:ops@example.test" }
capabilities = ["delivery"]
`

const defaultEmbeddingConnection = `
[connections."service/embedding"]
kind = "service/embedding"
label = "Embedding"
config = { url = "http://embed.invalid/v1", model = "embeddinggemma" }
capabilities = ["embeddings"]
`

const defaultQbitConnection = `
[connections."service/qbittorrent"]
kind = "service/qbittorrent"
label = "qBittorrent"
config = { url = "http://qbittorrent.invalid" }
capabilities = ["api"]
`

var defaultAgentConnection = func() string {
	dir := filepath.Join(testRoot, "host")
	token := filepath.Join(dir, "media-agent.token")
	if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(token, []byte(strings.Repeat("t", 40)+"\n"), 0o600) != nil {
		panic("cannot write the default media agent bearer")
	}
	return agentConnectionTOML("http://127.0.0.1:9", token)
}()

func hostConfigBody(webpush string) string {
	return hostConfigBodyWith("", webpush, filepath.Join(testRoot, "host", "anthropic.key"))
}

func hostConfigBodyWith(embedding, webpush, anthropicKey string) string {
	dir := filepath.Join(testRoot, "host")
	anthropic := ""
	if anthropicKey != "" {
		anthropic = fmt.Sprintf(`[connections."service/anthropic"]
kind = "service/anthropic"
label = "Anthropic"
capabilities = ["models"]
credential_files = { api_key = %q }

`, anthropicKey)
	}
	return anthropic + fmt.Sprintf(`[connections."service/grafana"]
kind = "service/grafana"
label = "Grafana"
config = { url = "http://grafana.invalid" }
capabilities = ["viewer"]
credential_files = { viewer_token = %q }
%s%s`, filepath.Join(dir, "grafana.key"), embedding, webpush)
}

func hostConnectionConfig(t *testing.T, dir, name, table string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(hostConfigBody(defaultWebPushConnection)+table), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func embeddingHost(t *testing.T, dir, url, model, ca string) string {
	t.Helper()
	return embeddingHostWith(t, dir, url, model, ca, "")
}

func embeddingHostWith(t *testing.T, dir, url, model, ca, key string) string {
	t.Helper()
	table := fmt.Sprintf(`
[connections."service/embedding"]
kind = "service/embedding"
label = "Embedding"
config = { url = %q, model = %q }
capabilities = ["embeddings"]
`, url, model)
	if ca != "" {
		table += fmt.Sprintf("transport = { ca_file = %q }\n", ca)
	}
	if key != "" {
		keyFile := filepath.Join(dir, "embedding.key")
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table += fmt.Sprintf("credential_files = { bearer = %q }\n", keyFile)
	}
	path := filepath.Join(dir, "host-embedding.toml")
	if err := os.WriteFile(path, []byte(hostConfigBodyWith(table, defaultWebPushConnection, filepath.Join(testRoot, "host", "anthropic.key"))), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ship24Host(t *testing.T, dir, url, key string) string {
	t.Helper()
	table := fmt.Sprintf(`
[connections."service/ship24"]
kind = "service/ship24"
label = "Ship24"
config = { url = %q }
capabilities = ["api"]
`, url)
	if key != "" {
		keyFile := filepath.Join(dir, "ship24.key")
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table += fmt.Sprintf("credential_files = { api_key = %q }\n", keyFile)
	}
	return hostConnectionConfig(t, dir, "host-ship24.toml", table)
}

var defaultHostConfig = func() string {
	dir := filepath.Join(testRoot, "host")
	if os.MkdirAll(dir, 0o700) != nil {
		panic("cannot create the default host directory")
	}
	anthropicKey := filepath.Join(dir, "anthropic.key")
	grafanaKey := filepath.Join(dir, "grafana.key")
	if os.WriteFile(anthropicKey, []byte("dummy-anthropic-key\n"), 0o600) != nil ||
		os.WriteFile(grafanaKey, []byte("dummy-grafana-token\n"), 0o600) != nil {
		panic("cannot write the default host credentials")
	}
	path := filepath.Join(dir, "host.toml")
	body := hostConfigBodyWith(defaultEmbeddingConnection, defaultWebPushConnection, anthropicKey) + defaultQbitConnection + defaultAgentConnection + mailConnection("icloud", "icloud", "operator@icloud.invalid", defaultMailPassword, true)
	if os.WriteFile(path, []byte(body), 0o600) != nil {
		panic("cannot write the default host configuration")
	}
	return path
}()

var testEnv = func() map[string]string {
	env := requirementEnv(filepath.Join(testRoot, "media"))
	env[anthropic.BaseURLEnv] = "http://127.0.0.1:9"
	env[settingEnv(ops.ListenKey)] = "127.0.0.1:0"
	return env
}()

type testVars struct {
	mu sync.Mutex
	m  map[string]string
}

var testVarsByTest sync.Map
var fixturesByTest sync.Map

func testKey(t *testing.T) string {
	name := t.Name()
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

func registerFixture(t *testing.T, f *fixture) {
	key := testKey(t)
	fixturesByTest.Store(t, f)
	fixturesByTest.Store(key, f)
	t.Cleanup(func() {
		fixturesByTest.Delete(t)
		fixturesByTest.CompareAndDelete(key, f)
	})
}

func fixtureFor(t *testing.T) *fixture {
	if v, ok := fixturesByTest.Load(t); ok {
		return v.(*fixture)
	}
	if v, ok := fixturesByTest.Load(testKey(t)); ok {
		return v.(*fixture)
	}
	return nil
}

func varsFor(t *testing.T) *testVars {
	if v, ok := testVarsByTest.Load(t); ok {
		return v.(*testVars)
	}
	if v, ok := testVarsByTest.Load(testKey(t)); ok {
		return v.(*testVars)
	}
	v := &testVars{m: maps.Clone(testEnv)}
	actual, _ := testVarsByTest.LoadOrStore(testKey(t), v)
	return actual.(*testVars)
}

func (v *testVars) snapshot() map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return maps.Clone(v.m)
}

func (v *testVars) lookup(name string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.m[name]
	return value, ok
}

func testLookupFor(t *testing.T) config.Env {
	v := varsFor(t)
	return func(name string) (string, bool) {
		if value, ok := v.lookup(name); ok {
			return value, true
		}
		return config.OS(name)
	}
}

func setEnv(t *testing.T, name, value string) {
	t.Helper()
	if f := fixtureFor(t); f != nil {
		old, had := f.set(name, value)
		t.Cleanup(func() {
			if had {
				f.set(name, old)
				return
			}
			f.unset(name)
		})
		return
	}
	v := varsFor(t)
	v.mu.Lock()
	old, had := v.m[name]
	v.m[name] = value
	v.mu.Unlock()
	t.Cleanup(func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		if had {
			v.m[name] = old
			return
		}
		delete(v.m, name)
	})
}

func seedSettings(t *testing.T, kv map[string]string) {
	t.Helper()
	if got := runSettings(t, "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list before seeding: exit %d stderr %q", got.code, got.stderr)
	}
	lookup := testLookupFor(t)
	if f := fixtureFor(t); f != nil {
		lookup = f.lookup
	}
	path, _ := lookup("ATRIUM_DB")
	if path == "" {
		t.Fatal("seeding settings needs ATRIUM_DB")
	}
	seedSettingsAt(t, path, kv)
}

func allFlagSettings() map[string]string {
	flags := map[string]string{}
	for _, name := range moduleNames {
		flags[module.FlagKey(name)] = "true"
	}
	return flags
}

func seedAllFlags(t *testing.T) {
	t.Helper()
	seedSettings(t, allFlagSettings())
}

func seedAllFlagsIn(t *testing.T, dir string) {
	t.Helper()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	seedAllFlags(t)
}

func flagKeyOf(key string) bool {
	for _, name := range moduleNames {
		if key == module.FlagKey(name) {
			return true
		}
	}
	return false
}

func newTestSet(env config.Env) *module.Set {
	cat, err := app.Catalog(app.Dependencies{Env: env}, module.Context{})
	if err != nil {
		panic(err)
	}
	set, err := module.NewSet(cat, cat.Settings(), env, nil)
	if err != nil {
		panic(err)
	}
	return set
}

func appEnv(t *testing.T) config.Env {
	if f := fixtureFor(t); f != nil {
		return f.lookup
	}
	return testLookupFor(t)
}

func bootedState(t *testing.T, env config.Env) (context.Context, *settings.Registry, *config.State) {
	t.Helper()
	reg := testSettings()
	st := config.NewState(reg)
	ctx := settings.With(config.With(context.Background(), st), reg)
	if err := bootstrap(env, reg)(ctx, io.Discard); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return ctx, reg, st
}

func appKey(t *testing.T, env config.Env) db.Key {
	t.Helper()
	_, _, st := bootedState(t, env)
	return st.DBKey()
}

func keyFor(t *testing.T) db.Key {
	t.Helper()
	return appKey(t, appEnv(t))
}

func seedSettingsAt(t *testing.T, path string, kv map[string]string) {
	t.Helper()
	seedSettingsWith(t, keyFor(t), path, kv)
}

func seedSettingsWith(t *testing.T, dbKey db.Key, path string, kv map[string]string) {
	t.Helper()
	env := testLookupFor(t)
	ctx, reg, _ := bootedState(t, env)
	d, err := db.Connect(path, db.WithKey(dbKey))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := newTestSet(env).Seed(ctx, d); err != nil {
		t.Fatalf("seed modules: %v", err)
	}
	ctx = audit.With(ctx, "fixture", audit.UI)
	for key, value := range kv {
		if key == "" {
			continue
		}
		if flagKeyOf(key) && value == "" {
			value = "false"
		}
		if err := reg.Set(ctx, d.Write, key, value); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
}

func plantSettingRow(t *testing.T, path, key, value string) {
	t.Helper()
	d, err := db.Connect(path, db.WithKey(keyFor(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value); err != nil {
		t.Fatalf("plant %s: %v", key, err)
	}
}

func hostConfigEdit(t *testing.T, dir, name string, edits map[string]string, tables ...string) string {
	t.Helper()
	return hostConfigFrom(t, defaultHostConfig, dir, name, edits, tables...)
}

func hostConfigFrom(t *testing.T, src, dir, name string, edits map[string]string, tables ...string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for old, next := range edits {
		if !strings.Contains(body, old) {
			t.Fatalf("host config has no %q to replace", old)
		}
		body = strings.Replace(body, old, next, 1)
	}
	for _, table := range tables {
		body += table
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hostConfigAppend(t *testing.T, src, dir, name string, tables ...string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw) + strings.Join(tables, "")
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func qbitHost(t *testing.T, dir, url string) string {
	t.Helper()
	return hostConfigEdit(t, dir, "host-qbit.toml", map[string]string{
		`config = { url = "http://qbittorrent.invalid" }`: `config = { url = ` + strconv.Quote(url) + ` }`,
	})
}

func qbitHostWithCA(t *testing.T, dir, url, ca string) string {
	t.Helper()
	return hostConfigEdit(t, dir, "host-qbit-ca.toml", map[string]string{
		`config = { url = "http://qbittorrent.invalid" }`: `config = { url = ` + strconv.Quote(url) + ` }`,
		`capabilities = ["api"]`:                          `capabilities = ["api"]` + "\ntransport = { ca_file = " + strconv.Quote(ca) + " }",
	})
}

func embedHost(t *testing.T, dir, url string) string {
	t.Helper()
	return embedHostNamed(t, dir, "host-embed.toml", url)
}

func embedHostNamed(t *testing.T, dir, name, url string) string {
	t.Helper()
	return hostConfigEdit(t, dir, name, map[string]string{
		`config = { url = "http://embed.invalid/v1", model = "embeddinggemma" }`: `config = { url = ` + strconv.Quote(url) + `, model = "embeddinggemma" }`,
	})
}

func weatherHost(t *testing.T, dir, forecast, geocoding string) string {
	t.Helper()
	return hostConfigEdit(t, dir, "host-weather.toml", nil, fmt.Sprintf(`
[connections."service/weather"]
kind = "service/weather"
label = "Weather"
config = { url = %q, geocoding_url = %q }
capabilities = ["forecast", "geocoding"]
`, forecast, geocoding))
}

func eztvTable(url string) string {
	return fmt.Sprintf(`
[connections."service/eztv"]
kind = "service/eztv"
label = "EZTV"
config = { url = %q }
capabilities = ["api"]
`, url)
}

func tmdbTable(url, imagesURL string) string {
	return fmt.Sprintf(`
[connections."service/tmdb"]
kind = "service/tmdb"
label = "TMDB"
config = { url = %q, images_url = %q }
capabilities = ["api"]
`, url, imagesURL)
}

func mediaHost(t *testing.T, dir, qbit, eztv, tmdb, tmdbImages string) string {
	t.Helper()
	edits := map[string]string{}
	if qbit != "" {
		edits[`config = { url = "http://qbittorrent.invalid" }`] = `config = { url = ` + strconv.Quote(qbit) + ` }`
	}
	var tables []string
	if eztv != "" {
		tables = append(tables, eztvTable(eztv))
	}
	if tmdb != "" {
		tables = append(tables, tmdbTable(tmdb, tmdbImages))
	}
	return hostConfigEdit(t, dir, "host-media.toml", edits, tables...)
}

func grafanaHost(t *testing.T, dir, url string) string {
	t.Helper()
	return grafanaHostWith(t, dir, url, "metrics", "", "")
}

func grafanaHostWith(t *testing.T, dir, url, metricsUID, logsUID, logsType string) string {
	t.Helper()
	raw, err := os.ReadFile(defaultHostConfig)
	if err != nil {
		t.Fatal(err)
	}
	config := `config = { url = ` + strconv.Quote(url) + `, metrics_uid = ` + strconv.Quote(metricsUID)
	if logsUID != "" {
		config += `, logs_uid = ` + strconv.Quote(logsUID)
	}
	if logsType != "" {
		config += `, logs_type = ` + strconv.Quote(logsType)
	}
	config += ` }`
	body := strings.Replace(string(raw), `config = { url = "http://grafana.invalid" }`, config, 1)
	path := filepath.Join(dir, "host-grafana.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func moduleEnv(dir string) map[string]string {
	return map[string]string{
		settingEnv(contacts.DirKey): filepath.Join(dir, "media-contacts"),
	}
}

func requirementEnv(mediaRoot string) map[string]string {
	return map[string]string{
		settingEnv(contacts.DirKey): mediaRoot + "-contacts",
		"ATRIUM_HOST_CONFIG":        defaultHostConfig,
	}
}

func init() {
	defaults := requirementEnv(filepath.Join(testRoot, "media"))
	defaults[anthropic.BaseURLEnv] = "http://127.0.0.1:9"
	defaults[settingEnv(ops.ListenKey)] = "127.0.0.1:0"
	for name, value := range defaults {
		if _, set := os.LookupEnv(name); !set {
			_ = os.Setenv(name, value)
		}
	}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			testEnv[name] = value
		}
	}
}

func quietLog(io.Writer) {}

var errExternalDNS = errors.New("external DNS is disabled in tests")

func guardExternalHosts() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errExternalDNS
		},
	}
}

func newTestApp(t *testing.T) *cli.App {
	return newAppWith(testLookupFor(t), imapclient.Opener, nil, quietLog)
}

func defaultApp(t *testing.T) *cli.App {
	if f := fixtureFor(t); f != nil {
		return f.app
	}
	return newTestApp(t)
}
