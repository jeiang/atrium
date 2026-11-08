package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/settings"
)

var moduleNames = module.Builtins

func settingEnv(key string) string {
	d, ok := settings.Default.Lookup(key)
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

var defaultAccountsFile = func() string {
	dir := filepath.Join(testRoot, "accounts")
	secret := filepath.Join(dir, "icloud.password")
	path := filepath.Join(dir, "accounts.toml")
	body := fmt.Sprintf("[[account]]\nid = \"icloud\"\npreset = \"icloud\"\nlogin = \"operator@icloud.invalid\"\npassword_file = %q\n", secret)
	if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(secret, []byte("dummy-icloud-password\n"), 0o600) != nil || os.WriteFile(path, []byte(body), 0o600) != nil {
		panic("cannot write the default mail accounts file")
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

func testLookup(name string) (string, bool) {
	if activeFixture != nil {
		if v, ok := activeFixture.env[name]; ok {
			return v, true
		}
	}
	if v, ok := testEnv[name]; ok {
		return v, true
	}
	return config.OS(name)
}

var testApp = newApp(testLookup, settings.Default, nil)

func setEnv(t *testing.T, name, value string) {
	t.Helper()
	target := testEnv
	if activeFixture != nil {
		target = activeFixture.env
	}
	old, had := target[name]
	target[name] = value
	t.Cleanup(func() {
		if had {
			target[name] = old
			return
		}
		delete(target, name)
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
		settingEnv(mail.KeyChatURL):            "http://chat.invalid/v1",
		settingEnv(mail.KeyEmbedURL):           "http://embed.invalid/v1",
		settingEnv(media.KeyRoot):              mediaRoot,
		settingEnv(media.KeyQbitURL):           "http://qbittorrent.invalid",
		settingEnv(notify.KeyURL):              "http://ntfy.invalid",
		settingEnv(notify.KeyTopic):            "atrium",
		settingEnv(calendar.AppleIDKey):        "operator@icloud.invalid",
		settingEnv(contacts.DirKey):            mediaRoot + "-contacts",
		settingEnv(config.KeyMailAccountsFile): defaultAccountsFile,
		"ANTHROPIC_API_KEY":                    "dummy-anthropic-key",
		settingEnv(notion.TokenKey):            "dummy-notion-token",
		settingEnv(grafanatools.KeyURL):        "http://grafana.invalid",
		settingEnv(grafanatools.KeyToken):      "dummy-grafana-token",
	}
}

func init() {
	defaults := requirementEnv(filepath.Join(testRoot, "media"))
	defaults[anthropic.BaseURLEnv] = "http://127.0.0.1:9"
	notion.UseBaseURL("http://127.0.0.1:9")
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

func defaultApp() *cli.App {
	if activeFixture != nil {
		return activeFixture.app
	}
	return testApp
}
