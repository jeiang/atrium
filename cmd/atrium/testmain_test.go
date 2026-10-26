package main

import (
	"os"
	"path/filepath"

	"github.com/jeiang/atrium/internal/anthropic"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/calendar"
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

var moduleNames = []string{module.Mail, module.Calendar, module.Notion, module.Media, module.Assistant, module.Notifications, module.Grafana, module.Contacts}

func settingEnv(key string) string {
	d, ok := settings.Default.Lookup(key)
	if !ok {
		panic("unregistered setting " + key)
	}
	return config.EnvName(d)
}

func flagEnv(name string) string { return settingEnv(module.FlagKey(name)) }

func requirementEnv(mediaRoot string) map[string]string {
	return map[string]string{
		settingEnv(mail.KeyChatURL):       "http://chat.invalid/v1",
		settingEnv(mail.KeyEmbedURL):      "http://embed.invalid/v1",
		settingEnv(media.KeyRoot):         mediaRoot,
		settingEnv(media.KeyQbitURL):      "http://qbittorrent.invalid",
		settingEnv(notify.KeyURL):         "http://ntfy.invalid",
		settingEnv(notify.KeyTopic):       "atrium",
		settingEnv(calendar.AppleIDKey):   "operator@icloud.invalid",
		settingEnv(contacts.DirKey):       mediaRoot + "-contacts",
		"ICLOUD_APP_PASSWORD":             "dummy-icloud-password",
		"ANTHROPIC_API_KEY":               "dummy-anthropic-key",
		settingEnv(notion.TokenKey):       "dummy-notion-token",
		settingEnv(grafanatools.KeyURL):   "http://grafana.invalid",
		settingEnv(grafanatools.KeyToken): "dummy-grafana-token",
	}
}

func init() {
	defaults := requirementEnv(filepath.Join(os.TempDir(), "atrium-test-media"))
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
}
