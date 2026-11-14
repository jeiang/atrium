package settings

import "strings"

var connectionOwnedHost = map[string]string{
	"anthropic_ca_file":      `connections."service/anthropic".transport.ca_file`,
	"anthropic_proxy":        `connections."service/anthropic".transport.proxy`,
	"grafana_url":            `connections."service/grafana".config.url`,
	"grafana_metrics_uid":    `connections."service/grafana".config.metrics_uid`,
	"grafana_logs_uid":       `connections."service/grafana".config.logs_uid`,
	"grafana_logs_type":      `connections."service/grafana".config.logs_type`,
	"grafana_logs_query":     `connections."service/grafana".config.logs_query`,
	"grafana_ca_file":        `connections."service/grafana".transport.ca_file`,
	"grafana_proxy":          `connections."service/grafana".transport.proxy`,
	"weather_open_meteo_url": `connections."service/weather".config.url`,
	"weather_geocoding_url":  `connections."service/weather".config.geocoding_url`,
	"open_meteo_ca_file":     `connections."service/weather".transport.ca_file`,
	"open_meteo_proxy":       `connections."service/weather".transport.proxy`,
	"media_tmdb_url":         `connections."service/tmdb".config.url`,
	"media_tmdb_images_url":  `connections."service/tmdb".config.images_url`,
	"tmdb_ca_file":           `connections."service/tmdb".transport.ca_file`,
	"tmdb_proxy":             `connections."service/tmdb".transport.proxy`,
	"media_eztv_url":         `connections."service/eztv".config.url`,
	"mail_embed_url":         `connections."service/embedding".config.url`,
	"mail_embed_model":       `connections."service/embedding".config.model`,
	"mail_embed_ca_file":     `connections."service/embedding".transport.ca_file`,
	"mail_embed_proxy":       `connections."service/embedding".transport.proxy`,
	"mail_ship24_url":        `connections."service/ship24".config.url`,
	"ship24_ca_file":         `connections."service/ship24".transport.ca_file`,
	"ship24_proxy":           `connections."service/ship24".transport.proxy`,
	"media_qbittorrent_url":  `connections."service/qbittorrent".config.url`,
	"qbittorrent_ca_file":    `connections."service/qbittorrent".transport.ca_file`,
	"qbittorrent_proxy":      `connections."service/qbittorrent".transport.proxy`,
	"oidc_preset":            `connections."service/oidc".config.preset`,
	"oidc_issuer":            `connections."service/oidc".config.issuer`,
	"oidc_client_id":         `connections."service/oidc".config.client_id`,
	"oidc_api_client_id":     `connections."service/oidc".config.api_client_id`,
	"oidc_scopes":            `connections."service/oidc".config.scopes`,
	"oidc_groups_claim":      `connections."service/oidc".config.groups_claim`,
	"oidc_access_groups":     `connections."service/oidc".config.access_groups`,
	"oidc_access_emails":     `connections."service/oidc".config.access_emails`,
	"oidc_access_domains":    `connections."service/oidc".config.access_domains`,
	"notion_ca_file":         `connections."service/notion".transport.ca_file`,
	"notion_proxy":           `connections."service/notion".transport.proxy`,
	"webpush_ca_file":        `connections."service/webpush".transport.ca_file`,
	"mail_accounts_file":     "the Connections page (mail account connections)",
}

func ConnectionOwnedHostKey(key string) bool {
	_, ok := connectionOwnedHost[key]
	return ok
}

var appKeyFiles = map[string]string{
	"action_link_key": "app_keys.action_link_key_file",
	"link_key":        "app_keys.link_key_file",
}

func Destination(entry ImportEntry) string {
	if entry.Class == ImportTuning {
		return "the compiled constant " + entry.Target
	}
	if entry.Action == ImportDrop && entry.Class == ImportOperator && entry.Module != "" {
		return entry.Target
	}
	if file, ok := appKeyFiles[entry.Key]; ok {
		return "the app key file " + file
	}
	if entry.Action == ImportRename {
		if destination, ok := connectionDestination(entry.Target); ok {
			return destination
		}
		return "the connection field " + entry.Target
	}
	if entry.Retired {
		return entry.Target
	}
	if entry.Class == ImportHost {
		if destination, ok := connectionOwnedHost[entry.Key]; ok {
			return destination
		}
		return "host configuration " + entry.Target
	}
	return "atrium settings set " + entry.Key
}

func connectionDestination(target string) (string, bool) {
	if id, slot, cut := strings.Cut(target, " credential "); cut {
		id = strings.TrimSuffix(id, " host")
		if strings.Contains(id, "/") && slot != "" {
			return `connections."` + id + `".credential_files.` + slot, true
		}
	}
	id, field, cut := strings.Cut(target, ".")
	if !cut || field == "" || !strings.Contains(id, "/") {
		return "", false
	}
	return `connections."` + id + `".` + field, true
}
