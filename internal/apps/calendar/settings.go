package calendar

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/unit"
)

const (
	ICloudCalendarKey   = "icloud_calendar"
	CalendarDirKey      = "calendar_dir"
	DefaultMinutesKey   = "default_event_minutes"
	defaultEventMinutes = "60"
)

func SettingsDefs() []settings.Def {
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{Key: davmirror.AppleIDKey, Module: unit.Calendar, Type: settings.String}),
		settings.Value(settings.UI, settings.Def{Key: davmirror.AccountKey, Module: unit.Calendar, Type: settings.String}),
		settings.Value(settings.UI, settings.Def{Key: ICloudCalendarKey, Module: unit.Calendar, Type: settings.String}),
		settings.Value(settings.UI, settings.Def{
			Key: DefaultMinutesKey, Module: unit.Calendar, Type: settings.Int, Default: defaultEventMinutes,
			Check: settings.Between(5, 1440)}),
	}
}

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: CalendarDirKey, Module: unit.Calendar, Type: settings.String, NixOption: "services.atrium.stateDir"},
	}
}

type Settings struct {
	davmirror.Login
	ICloudCalendar string
	CalendarDir    string
	MaxEvents      int
	MaxOccurrences int
	DefaultMinutes int
	Google         *connections.Lease
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
		func() error {
			var err error
			out.Login, err = davmirror.LoadLogin(ctx, q)
			return err
		},
		func() error { return text(ICloudCalendarKey, &out.ICloudCalendar) },
		func() error { out.CalendarDir = config.From(ctx).HostValue(CalendarDirKey); return nil },
		func() error { return whole(DefaultMinutesKey, &out.DefaultMinutes) },
	} {
		if err := step(); err != nil {
			return Settings{}, err
		}
	}
	out.ICloudCalendar = strings.ToLower(strings.TrimSpace(out.ICloudCalendar))
	out.Google = openConnection(ctx, q, connections.GoogleID)
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

func CandidateLimits(ctx context.Context, q db.Querier) (scan, draft int, err error) {
	return TuningCandidateScanLimit, TuningCandidateDraftLimit, nil
}

func missingCredentials(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return NotConfigured(strings.Join(missing, ", ") + " not set")
}
