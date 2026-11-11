package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/settings"
)

var moduleNames = module.Builtins

func settingEnv(key string) string {
	d, ok := settings.LookupOf(key)
	if !ok {
		panic("unregistered setting " + key)
	}
	return config.EnvName(d)
}

func flagEnv(name string) string { return settingEnv(module.FlagKey(name)) }

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

func hostConfigBody(webpush string) string {
	dir := filepath.Join(testRoot, "host")
	return fmt.Sprintf(`[connections."service/anthropic"]
kind = "service/anthropic"
label = "Anthropic"
capabilities = ["models"]
credential_files = { api_key = %q }

[connections."service/grafana"]
kind = "service/grafana"
label = "Grafana"
capabilities = ["viewer"]
credential_files = { viewer_token = %q }
%s`, filepath.Join(dir, "anthropic.key"), filepath.Join(dir, "grafana.key"), webpush)
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
	body := hostConfigBody(defaultWebPushConnection) + mailConnection("icloud", "icloud", "operator@icloud.invalid", defaultMailPassword, true)
	if os.WriteFile(path, []byte(body), 0o600) != nil {
		panic("cannot write the default host configuration")
	}
	return path
}()

var testEnv = func() map[string]string {
	env := requirementEnv(filepath.Join(testRoot, "media"))
	env[anthropic.BaseURLEnv] = "http://127.0.0.1:9"
	env[settingEnv(ops.ListenKey)] = "127.0.0.1:0"
	for _, name := range moduleNames {
		env[flagEnv(name)] = "true"
	}
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

func moduleEnv(dir string) map[string]string {
	return map[string]string{
		settingEnv(media.KeyRoot):   filepath.Join(dir, "media"),
		settingEnv(contacts.DirKey): filepath.Join(dir, "media-contacts"),
	}
}

func requirementEnv(mediaRoot string) map[string]string {
	return map[string]string{
		settingEnv(endpoint.KeyChatURL):        "http://chat.invalid/v1",
		settingEnv(endpoint.KeyEmbedURL):       "http://embed.invalid/v1",
		settingEnv(media.KeyRoot):              mediaRoot,
		settingEnv(media.KeyQbitURL):           "http://qbittorrent.invalid",
		settingEnv(calendar.AppleIDKey):        "operator@icloud.invalid",
		settingEnv(contacts.DirKey):            mediaRoot + "-contacts",
		"ATRIUM_HOST_CONFIG":                   defaultHostConfig,
		settingEnv(grafanatools.KeyURL):        "http://grafana.invalid",
	}
}

func init() {
	defaults := requirementEnv(filepath.Join(testRoot, "media"))
	defaults[anthropic.BaseURLEnv] = "http://127.0.0.1:9"
	defaults[settingEnv(ops.ListenKey)] = "127.0.0.1:0"
	for _, name := range moduleNames {
		defaults[flagEnv(name)] = "true"
	}
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

func newTestApp(t *testing.T, mods []module.Module) *cli.App {
	reg := settings.New()
	return newAppWith(testLookupFor(t), reg, config.NewState(reg), mods, imapclient.Opener, nil, quietLog)
}

func defaultApp(t *testing.T) *cli.App {
	if f := fixtureFor(t); f != nil {
		return f.app
	}
	return newTestApp(t, nil)
}
