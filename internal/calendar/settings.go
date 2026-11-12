package calendar

import (
	"context"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	AppleIDKey          = "icloud_apple_id"
	AccountKey          = "calendar_account"
	ICloudCalendarKey   = "icloud_calendar"
	CalendarDirKey      = "calendar_dir"
	DefaultMinutesKey   = "default_event_minutes"
	defaultEventMinutes = "60"
)

func init() {
	settings.MustRegisterHost(settings.HostDef{
		Key: CalendarDirKey, Module: "calendar", Type: settings.String})
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{Key: AppleIDKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.UI, settings.Def{Key: AccountKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.UI, settings.Def{Key: ICloudCalendarKey, Module: "calendar", Type: settings.String}),
		settings.Value(settings.UI, settings.Def{
			Key: DefaultMinutesKey, Module: "calendar", Type: settings.Int, Default: defaultEventMinutes,
			Check: settings.Between(5, 1440)}),
	)
}

type Settings struct {
	AppleID        string
	Account        string
	ICloudCalendar string
	CalendarDir    string
	MaxEvents      int
	MaxOccurrences int
	DefaultMinutes int
	Google         *connections.Lease
	DAV            *connections.Lease
}

func LoadSettings(ctx context.Context, q db.Querier) (Settings, error) {
	out := Settings{MaxEvents: TuningMaxEvents, MaxOccurrences: TuningMaxOccurrences}
	text := func(key string, into *string) error {
		found, err := settings.From(ctx).Resolve(ctx, q, key)
		*into = found.Value
		return err
	}
	whole := func(key string, into *int) error {
		found, err := settings.From(ctx).Resolve(ctx, q, key)
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
		func() error { out.CalendarDir = config.From(ctx).HostValue(CalendarDirKey); return nil },
		func() error { return whole(DefaultMinutesKey, &out.DefaultMinutes) },
	} {
		if err := step(); err != nil {
			return Settings{}, err
		}
	}
	out.AppleID = strings.TrimSpace(out.AppleID)
	out.Account = strings.TrimSpace(out.Account)
	out.ICloudCalendar = strings.ToLower(strings.TrimSpace(out.ICloudCalendar))
	out.Google = openConnection(ctx, q, connections.GoogleID)
	if account, err := icloudAccount(ctx, out); err == nil {
		out.DAV = openConnection(ctx, q, ICloudID(account.Name))
	}
	return out, nil
}

func openConnection(ctx context.Context, q db.Querier, id connections.ID) *connections.Lease {
	var (
		lease *connections.Lease
		err   error
	)
	if store := connections.DefaultFor(q); store != nil {
		lease, err = store.Open(ctx, id)
	} else {
		lease, err = connections.OpenRead(ctx, q, config.From(ctx).Host(), id)
	}
	if err != nil {
		return nil
	}
	return lease
}

func ICloudID(account string) connections.ID { return connections.ID("icloud/" + account) }

func CandidateLimits(ctx context.Context, q db.Querier) (scan, draft int, err error) {
	return TuningCandidateScanLimit, TuningCandidateDraftLimit, nil
}

func missingCredentials(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return NotConfigured(strings.Join(missing, ", ") + " not set")
}

func icloudAccount(ctx context.Context, set Settings) (config.AccountSpec, error) {
	var found []config.AccountSpec
	for _, a := range config.From(ctx).Accounts() {
		if a.Enabled && a.Preset == config.ICloud && (set.Account == "" || a.Name == set.Account) {
			found = append(found, a)
		}
	}
	switch {
	case len(found) == 0 && set.Account != "":
		return config.AccountSpec{}, NotConfigured(AccountKey + " " + strconv.Quote(set.Account) + " is not an enabled iCloud mail account")
	case len(found) == 0:
		return config.AccountSpec{}, NotConfigured("no enabled iCloud mail account")
	case len(found) > 1:
		return config.AccountSpec{}, NotConfigured("several iCloud mail accounts; set " + AccountKey)
	}
	return found[0], nil
}

func ICloudLogin(ctx context.Context, set Settings) (config.CalDAVLogin, error) {
	account, err := icloudAccount(ctx, set)
	if err != nil {
		return config.CalDAVLogin{}, err
	}
	apple := set.AppleID
	if apple == "" {
		apple = account.Login
	}
	password := ""
	if set.DAV != nil && set.DAV.ID() == ICloudID(account.Name) {
		if value, err := set.DAV.Credential("mail/" + account.Name + "/password"); err == nil {
			password = value
		}
	}
	if password == "" {
		password, _ = config.From(ctx).Password(account.Name)
	}
	if password == "" {
		return config.CalDAVLogin{}, NotConfigured("the iCloud app password is not set")
	}
	return config.CalDAVLogin{AppleID: apple, Password: password}, nil
}
