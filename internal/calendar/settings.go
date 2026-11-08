package calendar

import (
	"context"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	AppleIDKey          = "icloud_apple_id"
	AccountKey          = "calendar_account"
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
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{Key: AppleIDKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.UI, settings.Def{Key: AccountKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.UI, settings.Def{Key: ICloudCalendarKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.UI, settings.Def{Key: GoogleClientIDKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.UI, settings.Def{
			Key: DefaultMinutesKey, Module: "calendar", Type: settings.Int, Default: defaultEventMinutes,
			Check: settings.Between(5, 1440)}),
		settings.Value(settings.Advanced, settings.Def{Key: CalendarDirKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.Advanced, settings.Def{
			Key: MaxEventsKey, Module: "calendar", Type: settings.Int, Default: defaultMaxEvents,
			Check: settings.Between(1, 10000)}),
		settings.Value(settings.Advanced, settings.Def{
			Key: MaxOccurrencesKey, Module: "calendar", Type: settings.Int, Default: defaultMaxOccur,
			Check: settings.Between(1, 100000)}),
		settings.Value(settings.Advanced, settings.Def{
			Key: CandidateScanKey, Module: "calendar", Type: settings.Int, Default: "50",
			Check: settings.Between(1, 1000)}),
		settings.Value(settings.Advanced, settings.Def{
			Key: CandidateDraftKey, Module: "calendar", Type: settings.Int, Default: "10",
			Check: settings.Between(1, 100)}),
	)
}

type Settings struct {
	AppleID        string
	Account        string
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
		found, err := settings.Default.Resolve(ctx, q, key)
		*into = found.Value
		return err
	}
	whole := func(key string, into *int) error {
		found, err := settings.Default.Resolve(ctx, q, key)
		if err != nil {
			return err
		}
		n, err := found.Int()
		*into = int(n)
		return err
	}
	for _, step := range []func() error{
		func() error { return text(AppleIDKey, &out.AppleID) },
		func() error { return text(AccountKey, &out.Account) },
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
	out.ICloudCalendar = strings.ToLower(strings.TrimSpace(out.ICloudCalendar))
	return out, nil
}

func CandidateLimits(ctx context.Context, q db.Querier) (scan, draft int, err error) {
	for _, step := range []struct {
		key  string
		into *int
	}{{CandidateScanKey, &scan}, {CandidateDraftKey, &draft}} {
		found, err := settings.Default.Resolve(ctx, q, step.key)
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

func ICloudLogin(set Settings) (config.CalDAVLogin, error) {
	var found []config.AccountSpec
	for _, a := range config.Accounts() {
		if a.Enabled && a.Preset == config.ICloud && (set.Account == "" || a.Name == set.Account) {
			found = append(found, a)
		}
	}
	switch {
	case len(found) == 0 && set.Account != "":
		return config.CalDAVLogin{}, NotConfigured(AccountKey + " " + strconv.Quote(set.Account) + " is not an enabled iCloud mail account")
	case len(found) == 0:
		return config.CalDAVLogin{}, NotConfigured("no enabled iCloud mail account")
	case len(found) > 1:
		return config.CalDAVLogin{}, NotConfigured("several iCloud mail accounts; set " + AccountKey)
	}
	apple := set.AppleID
	if apple == "" {
		apple = found[0].Login
	}
	password, _ := config.Password(found[0].Name)
	return config.CalDAVLogin{AppleID: apple, Password: password}, nil
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
