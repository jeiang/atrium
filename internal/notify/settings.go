package notify

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	modules "github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	module = "notify"

	KeyPerRunCap  = "notify_per_run_cap"
	KeyNightlyCap = "notify_nightly_cap"
)

func Settings() []settings.Def {
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{
			Key: KeyPerRunCap, Label: "Notifications per run", Module: module, Type: settings.Int, Default: "3",
			Check: settings.Between(0, 100)}),
		settings.Value(settings.UI, settings.Def{
			Key: KeyNightlyCap, Label: "Notifications per night", Module: module, Type: settings.Int, Default: "1",
			Check: settings.Between(0, 100)}),
	}
}

func init() { settings.MustRegister(Settings()...) }

func Enabled(ctx context.Context) bool {
	return modules.IsEnabled(ctx, modules.Notifications)
}

func whole(ctx context.Context, q db.Querier, key string) (int, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	if err != nil {
		return 0, err
	}
	n, err := found.Int()
	return int(n), err
}
