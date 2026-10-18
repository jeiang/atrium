package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	_ "time/tzdata"

	"github.com/jeiang/atrium/internal/py"
)

const (
	Shadow = "shadow"
	Live   = "live"
)

var Modes = [...]string{Shadow, Live}

type Env func(name string) (string, bool)

var OS Env = os.LookupEnv

func Map(m map[string]string) Env {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

func get(env Env, name string) string {
	v, _ := env(name)
	return v
}

func stripped(env Env, name string) string { return py.Strip(get(env, name)) }

func cleanURL(env Env, name string) string { return strings.TrimRight(stripped(env, name), "/") }

func Mode(env Env) (string, error) {
	value, ok := env("ATRIUM_MODE")
	if !ok {
		value = Shadow
	}
	for _, m := range Modes {
		if value == m {
			return value, nil
		}
	}
	return "", fmt.Errorf("ATRIUM_MODE must be one of (%s, %s), got %s", py.Repr(Shadow), py.Repr(Live), py.Repr(value))
}

func TZName(env Env) string {
	if name := stripped(env, "ATRIUM_TZ"); name != "" {
		return name
	}
	return Timezone
}

func Zone(name string) (*time.Location, error) {
	if name != "" && name != "Local" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, nil
		}
	}
	return nil, fmt.Errorf("unknown timezone %s", py.Repr(name))
}

func Location(env Env) (*time.Location, error) { return Zone(TZName(env)) }

func Require(env Env, names ...string) error {
	var missing []string
	for _, n := range names {
		if get(env, n) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return NotConfigured(strings.Join(missing, ", ") + " not set")
	}
	return nil
}

func PublicHost(env Env) string { return stripped(env, "ATRIUM_PUBLIC_HOST") }

func CalendarDir(env Env) string { return get(env, "ATRIUM_CALENDAR_DIR") }

func ICloudCalendar(env Env) string { return strings.ToLower(stripped(env, "ATRIUM_ICLOUD_CALENDAR")) }

type CalDAVLogin struct {
	AppleID  string
	Password string
}

func ICloudCalDAV(env Env) (CalDAVLogin, error) {
	if err := Require(env, "ICLOUD_APPLE_ID", "ICLOUD_APP_PASSWORD"); err != nil {
		return CalDAVLogin{}, err
	}
	return CalDAVLogin{get(env, "ICLOUD_APPLE_ID"), get(env, "ICLOUD_APP_PASSWORD")}, nil
}

type GoogleClient struct {
	ID     string
	Secret string
}

func GoogleOAuthClient(env Env) GoogleClient {
	return GoogleClient{get(env, "GOOGLE_OAUTH_CLIENT_ID"), get(env, "GOOGLE_OAUTH_CLIENT_SECRET")}
}

type GoogleCredentials struct {
	GoogleClient
	RefreshToken string
}

func Google(env Env) (GoogleCredentials, error) {
	if err := Require(env, "GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET", "GOOGLE_REFRESH_TOKEN"); err != nil {
		return GoogleCredentials{}, err
	}
	return GoogleCredentials{GoogleOAuthClient(env), get(env, "GOOGLE_REFRESH_TOKEN")}, nil
}

func requireStripped(env Env, name string) (string, error) {
	if v := stripped(env, name); v != "" {
		return v, nil
	}
	return "", NotConfigured(name + " is not set")
}

func NotionToken(env Env) (string, error) { return requireStripped(env, "NOTION_TOKEN") }

func NotionWikiRoot(env Env) (string, error) { return requireStripped(env, "ATRIUM_NOTION_WIKI_ROOT") }

func AnthropicKey(env Env) (string, error) { return requireStripped(env, "ANTHROPIC_API_KEY") }

type GrafanaAccess struct {
	URL   string
	Token string
}

func Grafana(env Env) (GrafanaAccess, error) {
	g := GrafanaAccess{cleanURL(env, "GRAFANA_URL"), stripped(env, "GRAFANA_VIEWER_TOKEN")}
	if g.URL == "" || g.Token == "" {
		return GrafanaAccess{}, NotConfigured("GRAFANA_URL and GRAFANA_VIEWER_TOKEN must be set")
	}
	return g, nil
}

type NtfySettings struct {
	URL   string
	Topic string
	Token string
}

func Ntfy(env Env) NtfySettings {
	return NtfySettings{stripped(env, "NTFY_URL"), stripped(env, "NTFY_TOPIC"), stripped(env, "NTFY_TOKEN")}
}

type MediaSettings struct {
	Root      string
	QbitURL   string
	PublicURL string
	MoviesURL string
	TVURL     string
	NtfyTopic string
	TMDBKey   string
	LinkKey   string
}

func Media(env Env) MediaSettings {
	return MediaSettings{
		Root:      cleanURL(env, "ATRIUM_MEDIA_ROOT"),
		QbitURL:   cleanURL(env, "QBITTORRENT_URL"),
		PublicURL: cleanURL(env, "ATRIUM_PUBLIC_URL"),
		MoviesURL: cleanURL(env, "ATRIUM_MEDIA_MOVIES_URL"),
		TVURL:     cleanURL(env, "ATRIUM_MEDIA_TV_URL"),
		NtfyTopic: stripped(env, "NTFY_MEDIA_TOPIC"),
		TMDBKey:   stripped(env, "TMDB_API_KEY"),
		LinkKey:   stripped(env, "ATRIUM_MEDIA_LINK_KEY"),
	}
}

func (m MediaSettings) NeedRoot() (string, error) {
	if m.Root == "" {
		return "", NotConfigured("ATRIUM_MEDIA_ROOT is not set")
	}
	return m.Root, nil
}

func (m MediaSettings) NeedQbit() (string, error) {
	if m.QbitURL == "" {
		return "", NotConfigured("QBITTORRENT_URL is not set")
	}
	return m.QbitURL, nil
}
