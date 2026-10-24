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

type CalDAVLogin struct {
	AppleID  string
	Password string
}

type GoogleClient struct {
	ID     string
	Secret string
}

type GoogleCredentials struct {
	GoogleClient
	RefreshToken string
}

func GoogleOAuthSecret(env Env) string { return get(env, "GOOGLE_OAUTH_CLIENT_SECRET") }

func requireStripped(env Env, name string) (string, error) {
	if v := stripped(env, name); v != "" {
		return v, nil
	}
	return "", NotConfigured(name + " is not set")
}

func NotionToken(env Env) (string, error) { return requireStripped(env, "NOTION_TOKEN") }

func NotionWikiRoot(env Env) (string, error) { return requireStripped(env, "ATRIUM_NOTION_WIKI_ROOT") }

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

func NtfyToken(env Env) string { return stripped(env, "NTFY_TOKEN") }

func TMDBKey(env Env) string { return stripped(env, "TMDB_API_KEY") }
