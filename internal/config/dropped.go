package config

import (
	"strings"

	"github.com/jeiang/atrium/internal/settings"
)

type Dropped struct {
	Old string
	New string
}

var droppedNames = [...]struct{ old, key string }{
	{"ATRIUM_MODE", "mail_mode"},
	{"ATRIUM_TZ", KeyTZ},
	{"ATRIUM_PUBLIC_URL", KeyPublicURL},
	{"ATRIUM_CALENDAR_DIR", "calendar_dir"},
	{"ATRIUM_ICLOUD_CALENDAR", "icloud_calendar"},
	{"ICLOUD_APPLE_ID", "icloud_apple_id"},
	{"GOOGLE_OAUTH_CLIENT_ID", "google_client_id"},
	{"ATRIUM_CONTACTS_DIR", "contacts_dir"},
	{"ATRIUM_CHAT_URL", "mail_chat_url"},
	{"ATRIUM_EMBED_URL", "mail_embed_url"},
	{"NTFY_URL", "ntfy_url"},
	{"NTFY_TOPIC", "ntfy_topic"},
	{"NTFY_MEDIA_TOPIC", "ntfy_media_topic"},
	{"ATRIUM_MEDIA_ROOT", "media_root"},
	{"QBITTORRENT_URL", "media_qbittorrent_url"},
	{"ATRIUM_MEDIA_MOVIES_URL", "media_movies_url"},
	{"ATRIUM_MEDIA_TV_URL", "media_tv_url"},
	{"TMDB_API_KEY", "media_tmdb_token"},
	{"NOTION_TOKEN", "notion_token"},
	{"GRAFANA_URL", "grafana_url"},
	{"GRAFANA_VIEWER_TOKEN", "grafana_viewer_token"},
	{"ATRIUM_CODEMODE_SOCKET", ""},
	{"ATRIUM_ASSISTANT_MAX_RUN_COST_USD", "max_run_cost_usd_*"},
}

func DroppedEnv(env Env, reg *settings.Registry) []Dropped {
	var out []Dropped
	for _, d := range droppedNames {
		if _, set := env(d.old); !set {
			continue
		}
		var names []string
		if prefix, wild := strings.CutSuffix(d.key, "*"); wild {
			for _, def := range reg.Defs() {
				if strings.HasPrefix(def.Key, prefix) {
					names = append(names, EnvName(def))
				}
			}
		} else if def, ok := reg.Lookup(d.key); ok {
			names = append(names, EnvName(def))
		}
		replacement := strings.Join(names, ", ")
		out = append(out, Dropped{Old: d.old, New: replacement})
	}
	return out
}
