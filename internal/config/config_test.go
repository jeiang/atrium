package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func base(extra map[string]string) map[string]string {
	m := map[string]string{"ATRIUM_DB": "/s/atrium.db", "ATRIUM_CACHE_DIR": "/s/cache/messages"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func mustLoad(t *testing.T, m map[string]string) Config {
	t.Helper()
	cfg, err := Load(Map(m))
	if err != nil {
		t.Fatalf("Load(%v): %v", m, err)
	}
	return cfg
}

func val[T any](v T, err error) any {
	if err != nil {
		return err.Error()
	}
	return v
}

func TestEnvReaders(t *testing.T) {
	load := func(f func(Config) any) func(Env) any {
		return func(e Env) any {
			fallback := Map(base(nil))
			cfg, err := Load(func(name string) (string, bool) {
				if v, ok := e(name); ok {
					return v, true
				}
				return fallback(name)
			})
			if err != nil {
				return err.Error()
			}
			return f(cfg)
		}
	}
	cred := func(account string) func(Config) any {
		return func(c Config) any {
			u, p, ok := c.Credential(account)
			if !ok {
				return nil
			}
			return [2]string{u, p}
		}
	}
	tests := []struct {
		name  string
		env   map[string]string
		read  func(Env) any
		unset any
		want  any
	}{
		{"STATE_DIRECTORY", map[string]string{"STATE_DIRECTORY": "/var/lib/atrium:/other"},
			func(e Env) any {
				m := map[string]string{}
				if v, ok := e("STATE_DIRECTORY"); ok {
					m["STATE_DIRECTORY"] = v
				}
				c, err := Load(Map(m))
				if err != nil {
					return err.Error()
				}
				return [2]string{c.DBPath, c.CacheDir}
			},
			"ATRIUM_DB or STATE_DIRECTORY is required", [2]string{"/var/lib/atrium/atrium.db", "/var/lib/atrium/cache"}},
		{"ATRIUM_DB", map[string]string{"ATRIUM_DB": "/tmp/x/my.db"}, load(func(c Config) any { return c.DBPath }), "/s/atrium.db", "/tmp/x/my.db"},
		{"ATRIUM_CACHE_DIR", map[string]string{"ATRIUM_CACHE_DIR": "/c"}, load(func(c Config) any { return c.CacheDir }), "/s/cache/messages", "/c"},
		{"ATRIUM_MODE", map[string]string{"ATRIUM_MODE": "live"}, func(e Env) any { return val(Mode(e)) }, "shadow", "live"},
		{"ATRIUM_CHAT_URL", map[string]string{"ATRIUM_CHAT_URL": "http://chat/v1//"}, load(func(c Config) any { return c.ChatURL }), "", "http://chat/v1"},
		{"ATRIUM_EMBED_URL", map[string]string{"ATRIUM_EMBED_URL": "http://embed/v1/"}, load(func(c Config) any { return c.EmbedURL }), "", "http://embed/v1"},
		{"ATRIUM_CONTACTS_DIR", map[string]string{"ATRIUM_CONTACTS_DIR": "/s/vdirsyncer/contacts"}, load(func(c Config) any { return c.ContactsDir }), "", "/s/vdirsyncer/contacts"},
		{"ATRIUM_STATIC_DIR", map[string]string{"ATRIUM_STATIC_DIR": "/nix/store/x-static"}, load(func(c Config) any { return c.StaticDir }), "", "/nix/store/x-static"},
		{"ICLOUD_MAIL_USERNAME", map[string]string{"ICLOUD_MAIL_USERNAME": "me", "ICLOUD_APP_PASSWORD": "fake-icloud-pass"}, load(cred(ICloud)), nil, [2]string{"me", "fake-icloud-pass"}},
		{"ICLOUD_APP_PASSWORD", map[string]string{"ICLOUD_MAIL_USERNAME": "me", "ICLOUD_APP_PASSWORD": " fake icloud\tpass\nword "}, load(cred(ICloud)), nil, [2]string{"me", "fakeicloudpassword"}},
		{"GMAIL_ADDRESS", map[string]string{"GMAIL_ADDRESS": "a@gmail.com", "GMAIL_APP_PASSWORD": "fake-pass"}, load(cred(Gmail)), nil, [2]string{"a@gmail.com", "fake-pass"}},
		{"GMAIL_APP_PASSWORD", map[string]string{"GMAIL_ADDRESS": "a@gmail.com", "GMAIL_APP_PASSWORD": "fake gmail pass"}, load(cred(Gmail)), nil, [2]string{"a@gmail.com", "fakegmailpass"}},
		{"ATRIUM_TZ", map[string]string{"ATRIUM_TZ": " Asia/Tokyo "}, func(e Env) any { return TZName(e) }, "America/Port_of_Spain", "Asia/Tokyo"},
		{"ATRIUM_PUBLIC_HOST", map[string]string{"ATRIUM_PUBLIC_HOST": " atrium.example "}, func(e Env) any { return PublicHost(e) }, "", "atrium.example"},
		{"ATRIUM_PUBLIC_URL", map[string]string{"ATRIUM_PUBLIC_URL": " https://a.example/ "}, func(e Env) any { return Media(e).PublicURL }, "", "https://a.example"},
		{"ATRIUM_CALENDAR_DIR", map[string]string{"ATRIUM_CALENDAR_DIR": "/s/vdirsyncer/calendars"}, func(e Env) any { return CalendarDir(e) }, "", "/s/vdirsyncer/calendars"},
		{"ATRIUM_ICLOUD_CALENDAR", map[string]string{"ATRIUM_ICLOUD_CALENDAR": " Home "}, func(e Env) any { return ICloudCalendar(e) }, "", "home"},
		{"ICLOUD_APPLE_ID", map[string]string{"ICLOUD_APPLE_ID": "me@icloud.com", "ICLOUD_APP_PASSWORD": "fake-pass"}, func(e Env) any { return val(ICloudCalDAV(e)) }, "ICLOUD_APPLE_ID, ICLOUD_APP_PASSWORD not set", CalDAVLogin{"me@icloud.com", "fake-pass"}},
		{"GOOGLE_OAUTH_CLIENT_ID", map[string]string{"GOOGLE_OAUTH_CLIENT_ID": "id", "GOOGLE_OAUTH_CLIENT_SECRET": "fake-client-value"}, func(e Env) any { return GoogleOAuthClient(e) }, GoogleClient{}, GoogleClient{"id", "fake-client-value"}},
		{"GOOGLE_OAUTH_CLIENT_SECRET", map[string]string{"GOOGLE_OAUTH_CLIENT_ID": "id", "GOOGLE_OAUTH_CLIENT_SECRET": "fake-client-value", "GOOGLE_REFRESH_TOKEN": "fake-refresh-value"}, func(e Env) any { return val(Google(e)) }, "GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, GOOGLE_REFRESH_TOKEN not set", GoogleCredentials{GoogleClient{"id", "fake-client-value"}, "fake-refresh-value"}},
		{"GOOGLE_REFRESH_TOKEN", map[string]string{"GOOGLE_OAUTH_CLIENT_ID": "id", "GOOGLE_OAUTH_CLIENT_SECRET": "fake-client-value"}, func(e Env) any { return val(Google(e)) }, "GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, GOOGLE_REFRESH_TOKEN not set", "GOOGLE_REFRESH_TOKEN not set"},
		{"NOTION_TOKEN", map[string]string{"NOTION_TOKEN": " fake-notion-value "}, func(e Env) any { return val(NotionToken(e)) }, "NOTION_TOKEN is not set", "fake-notion-value"},
		{"ATRIUM_NOTION_WIKI_ROOT", map[string]string{"ATRIUM_NOTION_WIKI_ROOT": " 0123456789abcdef0123456789abcdef "}, func(e Env) any { return val(NotionWikiRoot(e)) }, "ATRIUM_NOTION_WIKI_ROOT is not set", "0123456789abcdef0123456789abcdef"},
		{"ANTHROPIC_API_KEY", map[string]string{"ANTHROPIC_API_KEY": " fake-anthropic-value "}, func(e Env) any { return val(AnthropicKey(e)) }, "ANTHROPIC_API_KEY is not set", "fake-anthropic-value"},
		{"GRAFANA_URL", map[string]string{"GRAFANA_URL": " https://g.example/ ", "GRAFANA_VIEWER_TOKEN": "fake-grafana-value"}, func(e Env) any { return val(Grafana(e)) }, "GRAFANA_URL and GRAFANA_VIEWER_TOKEN must be set", GrafanaAccess{"https://g.example", "fake-grafana-value"}},
		{"GRAFANA_VIEWER_TOKEN", map[string]string{"GRAFANA_URL": "https://g.example"}, func(e Env) any { return val(Grafana(e)) }, "GRAFANA_URL and GRAFANA_VIEWER_TOKEN must be set", "GRAFANA_URL and GRAFANA_VIEWER_TOKEN must be set"},
		{"NTFY_URL", map[string]string{"NTFY_URL": " https://ntfy.example/ "}, func(e Env) any { return Ntfy(e).URL }, "", "https://ntfy.example/"},
		{"NTFY_TOPIC", map[string]string{"NTFY_TOPIC": " atrium "}, func(e Env) any { return Ntfy(e).Topic }, "", "atrium"},
		{"NTFY_TOKEN", map[string]string{"NTFY_TOKEN": " fake-ntfy-value "}, func(e Env) any { return Ntfy(e).Token }, "", "fake-ntfy-value"},
		{"NTFY_MEDIA_TOPIC", map[string]string{"NTFY_MEDIA_TOPIC": " media "}, func(e Env) any { return Media(e).NtfyTopic }, "", "media"},
		{"ATRIUM_MEDIA_ROOT", map[string]string{"ATRIUM_MEDIA_ROOT": " /srv/media/ "}, func(e Env) any { return val(Media(e).NeedRoot()) }, "ATRIUM_MEDIA_ROOT is not set", "/srv/media"},
		{"QBITTORRENT_URL", map[string]string{"QBITTORRENT_URL": "http://qbit:8080/"}, func(e Env) any { return val(Media(e).NeedQbit()) }, "QBITTORRENT_URL is not set", "http://qbit:8080"},
		{"ATRIUM_MEDIA_MOVIES_URL", map[string]string{"ATRIUM_MEDIA_MOVIES_URL": "https://m.example//"}, func(e Env) any { return Media(e).MoviesURL }, "", "https://m.example"},
		{"ATRIUM_MEDIA_TV_URL", map[string]string{"ATRIUM_MEDIA_TV_URL": " https://tv.example/ "}, func(e Env) any { return Media(e).TVURL }, "", "https://tv.example"},
		{"TMDB_API_KEY", map[string]string{"TMDB_API_KEY": " fake-tmdb-value "}, func(e Env) any { return Media(e).TMDBKey }, "", "fake-tmdb-value"},
		{"ATRIUM_MEDIA_LINK_KEY", map[string]string{"ATRIUM_MEDIA_LINK_KEY": " fake-link-value "}, func(e Env) any { return Media(e).LinkKey }, "", "fake-link-value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := tt.env[tt.name]; !ok && tt.name != "GOOGLE_REFRESH_TOKEN" && tt.name != "GRAFANA_VIEWER_TOKEN" {
				t.Fatalf("row does not set %s", tt.name)
			}
			if got := tt.read(Map(nil)); !reflect.DeepEqual(got, tt.unset) {
				t.Errorf("unset: got %#v, want %#v", got, tt.unset)
			}
			if got := tt.read(Map(tt.env)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("set: got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLoadFallbacks(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		db      string
		cache   string
		wantErr string
	}{
		{"nothing", map[string]string{}, "", "", "ATRIUM_DB or STATE_DIRECTORY is required"},
		{"db only", map[string]string{"ATRIUM_DB": "/a/atrium.db"}, "", "", "ATRIUM_CACHE_DIR or STATE_DIRECTORY is required"},
		{"empty db uses state", map[string]string{"ATRIUM_DB": "", "STATE_DIRECTORY": "/st"}, "/st/atrium.db", "/st/cache", ""},
		{"leading colon state", map[string]string{"STATE_DIRECTORY": ":/st"}, "", "", "ATRIUM_DB or STATE_DIRECTORY is required"},
		{"explicit overrides state", map[string]string{"STATE_DIRECTORY": "/st", "ATRIUM_DB": "/d/x.db", "ATRIUM_CACHE_DIR": "/c/"}, "/d/x.db", "/c", ""},
		{"invalid mode", map[string]string{"STATE_DIRECTORY": "/st", "ATRIUM_MODE": "dry"}, "", "", "ATRIUM_MODE must be one of ('shadow', 'live'), got 'dry'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(Map(tt.env))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DBPath != tt.db || cfg.CacheDir != tt.cache {
				t.Errorf("got (%s, %s), want (%s, %s)", cfg.DBPath, cfg.CacheDir, tt.db, tt.cache)
			}
		})
	}
}

func TestModeMirrorsSafety(t *testing.T) {
	tests := []struct {
		env     map[string]string
		want    string
		wantErr string
	}{
		{map[string]string{}, Shadow, ""},
		{map[string]string{"ATRIUM_MODE": "shadow"}, Shadow, ""},
		{map[string]string{"ATRIUM_MODE": "live"}, Live, ""},
		{map[string]string{"ATRIUM_MODE": "dry"}, "", "ATRIUM_MODE must be one of ('shadow', 'live'), got 'dry'"},
		{map[string]string{"ATRIUM_MODE": ""}, "", "ATRIUM_MODE must be one of ('shadow', 'live'), got ''"},
		{map[string]string{"ATRIUM_MODE": "Live"}, "", "ATRIUM_MODE must be one of ('shadow', 'live'), got 'Live'"},
		{map[string]string{"ATRIUM_MODE": " live"}, "", "ATRIUM_MODE must be one of ('shadow', 'live'), got ' live'"},
		{map[string]string{"ATRIUM_MODE": "it's"}, "", `ATRIUM_MODE must be one of ('shadow', 'live'), got "it's"`},
	}
	for _, tt := range tests {
		got, err := Mode(Map(tt.env))
		if tt.wantErr != "" {
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("Mode(%v) err = %v, want %q", tt.env, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Mode(%v) = %q, %v; want %q", tt.env, got, err, tt.want)
		}
	}
}

func TestModeReadsLiveEnvironment(t *testing.T) {
	t.Setenv("ATRIUM_DB", "/s/atrium.db")
	t.Setenv("ATRIUM_CACHE_DIR", "/s/cache")
	t.Setenv("ATRIUM_MODE", "live")
	cfg, err := Load(OS)
	if err != nil || cfg.Mode != Live {
		t.Fatalf("Load = %v, %v", cfg.Mode, err)
	}
	t.Setenv("ATRIUM_MODE", "shadow")
	if m, err := Mode(OS); err != nil || m != Shadow {
		t.Fatalf("Mode after change = %q, %v; want shadow", m, err)
	}
	t.Setenv("ATRIUM_MODE", "dry")
	if _, err := Mode(OS); err == nil {
		t.Fatal("Mode accepted dry")
	}
}

func TestZone(t *testing.T) {
	tests := []struct {
		name    string
		wantErr string
	}{
		{"America/Port_of_Spain", ""},
		{"Asia/Tokyo", ""},
		{"UTC", ""},
		{"Mars/Olympus", "unknown timezone 'Mars/Olympus'"},
		{"Local", "unknown timezone 'Local'"},
		{"", "unknown timezone ''"},
		{"../etc/passwd", "unknown timezone '../etc/passwd'"},
	}
	for _, tt := range tests {
		loc, err := Zone(tt.name)
		if tt.wantErr != "" {
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("Zone(%q) err = %v, want %q", tt.name, err, tt.wantErr)
			}
			continue
		}
		if err != nil || loc.String() != tt.name {
			t.Errorf("Zone(%q) = %v, %v", tt.name, loc, err)
		}
	}
	loc, err := Location(Map(map[string]string{"ATRIUM_TZ": "   "}))
	if err != nil || loc.String() != Timezone {
		t.Errorf("Location(blank) = %v, %v; want %s", loc, err, Timezone)
	}
}

func TestSiblingPaths(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "custom", "test.sqlite")
	cfg := mustLoad(t, map[string]string{"ATRIUM_DB": db, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache", "messages")})
	parent := filepath.Join(dir, "custom")
	got := map[string]string{
		"pause":          cfg.PausePath(),
		"files":          cfg.FilesDir(),
		"media-link.key": cfg.MediaLinkKeyPath(),
		"db lock":        cfg.LockPath(),
		"notion lock":    cfg.NotionLockPath(),
		"media cache":    cfg.MediaCacheDir(),
	}
	want := map[string]string{
		"pause":          filepath.Join(parent, "pause"),
		"files":          filepath.Join(parent, "files"),
		"media-link.key": filepath.Join(parent, "media-link.key"),
		"db lock":        filepath.Join(parent, "test.sqlite.lock"),
		"notion lock":    filepath.Join(parent, "notion.lock"),
		"media cache":    filepath.Join(dir, "cache", "messages", "media"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	rel := mustLoad(t, base(map[string]string{"ATRIUM_DB": "atrium.db"}))
	if rel.PausePath() != "pause" || rel.LockPath() != "atrium.db.lock" {
		t.Errorf("relative db: pause %q lock %q", rel.PausePath(), rel.LockPath())
	}
	trailing := mustLoad(t, base(map[string]string{"ATRIUM_DB": "/a/b/"}))
	if trailing.PausePath() != "/a/pause" {
		t.Errorf("trailing slash pause = %q", trailing.PausePath())
	}
}

func TestCredentialAbsent(t *testing.T) {
	tests := []map[string]string{
		{},
		{"ICLOUD_MAIL_USERNAME": "me"},
		{"ICLOUD_MAIL_USERNAME": "me", "ICLOUD_APP_PASSWORD": " \t "},
		{"ICLOUD_MAIL_USERNAME": "", "ICLOUD_APP_PASSWORD": "fake-pass"},
	}
	for _, env := range tests {
		if _, _, ok := mustLoad(t, base(env)).Credential(ICloud); ok {
			t.Errorf("Credential(icloud) with %v: ok", env)
		}
	}
	if _, _, ok := mustLoad(t, base(nil)).Credential("yahoo"); ok {
		t.Error("unknown account has credentials")
	}
}

func TestAccounts(t *testing.T) {
	want := []AccountSpec{
		{"icloud", "imap.mail.me.com", "ICLOUD_MAIL_USERNAME", "ICLOUD_APP_PASSWORD", "Misc"},
		{"gmail", "imap.gmail.com", "GMAIL_ADDRESS", "GMAIL_APP_PASSWORD", ""},
	}
	if !reflect.DeepEqual(Accounts[:], want) {
		t.Errorf("Accounts = %v", Accounts)
	}
}

func TestConfigHidesCredentials(t *testing.T) {
	cfg := mustLoad(t, base(map[string]string{"ICLOUD_APP_PASSWORD": "fake-hidden-value"}))
	for _, s := range []string{cfg.String(), cfg.LogValue().String()} {
		if strings.Contains(s, "fake-hidden-value") {
			t.Errorf("credential leaked: %s", s)
		}
	}
}

func TestNotConfiguredIsTyped(t *testing.T) {
	_, err := AnthropicKey(Map(nil))
	var nc NotConfigured
	if !errors.As(err, &nc) {
		t.Fatalf("err %T is not NotConfigured", err)
	}
}

func TestPyRepr(t *testing.T) {
	tests := map[string]string{
		"dry":     "'dry'",
		"it's":    `"it's"`,
		`a'b"c`:   `'a\'b"c'`,
		"a\\b":    `'a\\b'`,
		"x\ny\tz": `'x\ny\tz'`,
		"\x00":    `'\x00'`,
		"\u00a0":  `'\xa0'`,
		"é":       "'é'",
	}
	for in, want := range tests {
		if got := pyRepr(in); got != want {
			t.Errorf("pyRepr(%q) = %s, want %s", in, got, want)
		}
	}
}
