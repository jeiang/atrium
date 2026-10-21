package calendar

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	AppleIDKey          = "icloud_apple_id"
	ICloudCalendarKey   = "icloud_calendar"
	CalendarDirKey      = "calendar_dir"
	GoogleClientIDKey   = "google_client_id"
	MaxEventsKey        = "max_events"
	MaxOccurrencesKey   = "max_occurrences"
	DefaultMinutesKey   = "default_event_minutes"
	CandidateScanKey    = "candidate_scan_limit"
	CandidateDraftKey   = "candidate_draft_limit"
	defaultMaxEvents    = "200"
	defaultMaxOccur     = "2000"
	defaultEventMinutes = "60"
)

func init() {
	instance := []settings.Scope{settings.Instance}
	settings.MustRegister(
		settings.Def{Key: AppleIDKey, Module: "calendar", Type: settings.String, Class: settings.UI, Scopes: instance},
		settings.Def{Key: ICloudCalendarKey, Module: "calendar", Type: settings.String, Class: settings.UI, Scopes: instance},
		settings.Def{Key: GoogleClientIDKey, Module: "calendar", Type: settings.String, Class: settings.UI, Scopes: instance},
		settings.Def{
			Key: DefaultMinutesKey, Module: "calendar", Type: settings.Int, Default: defaultEventMinutes,
			Check: settings.Between(5, 1440), Class: settings.UI, Scopes: instance,
		},
		settings.Def{Key: CalendarDirKey, Module: "calendar", Type: settings.String, Class: settings.Advanced, Scopes: instance},
		settings.Def{
			Key: MaxEventsKey, Module: "calendar", Type: settings.Int, Default: defaultMaxEvents,
			Check: settings.Between(1, 10000), Class: settings.Advanced, Scopes: instance,
		},
		settings.Def{
			Key: MaxOccurrencesKey, Module: "calendar", Type: settings.Int, Default: defaultMaxOccur,
			Check: settings.Between(1, 100000), Class: settings.Advanced, Scopes: instance,
		},
		settings.Def{
			Key: CandidateScanKey, Module: "calendar", Type: settings.Int, Default: "50",
			Check: settings.Between(1, 1000), Class: settings.Advanced, Scopes: instance,
		},
		settings.Def{
			Key: CandidateDraftKey, Module: "calendar", Type: settings.Int, Default: "10",
			Check: settings.Between(1, 100), Class: settings.Advanced, Scopes: instance,
		},
	)
}

type Settings struct {
	AppleID        string
	ICloudCalendar string
	CalendarDir    string
	GoogleClientID string
	MaxEvents      int
	MaxOccurrences int
	DefaultMinutes int
}

func LoadSettings(ctx context.Context, q db.Querier) (Settings, error) {
	var out Settings
	text := func(key string, into *string) error {
		found, err := settings.Default.Resolve(ctx, q, key, 0)
		*into = found.Value
		return err
	}
	whole := func(key string, into *int) error {
		found, err := settings.Default.Resolve(ctx, q, key, 0)
		if err != nil {
			return err
		}
		n, err := found.Int()
		*into = int(n)
		return err
	}
	for _, step := range []func() error{
		func() error { return text(AppleIDKey, &out.AppleID) },
		func() error { return text(ICloudCalendarKey, &out.ICloudCalendar) },
		func() error { return text(CalendarDirKey, &out.CalendarDir) },
		func() error { return text(GoogleClientIDKey, &out.GoogleClientID) },
		func() error { return whole(MaxEventsKey, &out.MaxEvents) },
		func() error { return whole(MaxOccurrencesKey, &out.MaxOccurrences) },
		func() error { return whole(DefaultMinutesKey, &out.DefaultMinutes) },
	} {
		if err := step(); err != nil {
			return Settings{}, err
		}
	}
	out.ICloudCalendar = strings.ToLower(py.Strip(out.ICloudCalendar))
	return out, nil
}

func CandidateLimits(ctx context.Context, q db.Querier) (scan, draft int, err error) {
	for _, step := range []struct {
		key  string
		into *int
	}{{CandidateScanKey, &scan}, {CandidateDraftKey, &draft}} {
		found, err := settings.Default.Resolve(ctx, q, step.key, 0)
		if err != nil {
			return 0, 0, err
		}
		n, err := found.Int()
		if err != nil {
			return 0, 0, err
		}
		*step.into = int(n)
	}
	return scan, draft, nil
}

func missingCredentials(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return NotConfigured(strings.Join(missing, ", ") + " not set")
}

func icloudLogin(env config.Env, set Settings) (config.CalDAVLogin, error) {
	password, _ := env("ICLOUD_APP_PASSWORD")
	var missing []string
	if set.AppleID == "" {
		missing = append(missing, "ATRIUM_CALENDAR_ICLOUD_APPLE_ID")
	}
	if password == "" {
		missing = append(missing, "ICLOUD_APP_PASSWORD")
	}
	return config.CalDAVLogin{AppleID: set.AppleID, Password: password}, missingCredentials(missing)
}

func googleCredentials(env config.Env, set Settings) (config.GoogleCredentials, error) {
	secret := config.GoogleOAuthSecret(env)
	refresh, _ := env("GOOGLE_REFRESH_TOKEN")
	var missing []string
	if set.GoogleClientID == "" {
		missing = append(missing, "ATRIUM_CALENDAR_GOOGLE_CLIENT_ID")
	}
	if secret == "" {
		missing = append(missing, "GOOGLE_OAUTH_CLIENT_SECRET")
	}
	if refresh == "" {
		missing = append(missing, "GOOGLE_REFRESH_TOKEN")
	}
	creds := config.GoogleCredentials{GoogleClient: config.GoogleClient{ID: set.GoogleClientID, Secret: secret}, RefreshToken: refresh}
	return creds, missingCredentials(missing)
}
