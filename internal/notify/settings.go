package notify

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	modules "github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	module = "notify"

	KeyPerRunCap  = "notify_per_run_cap"
	KeyNightlyCap = "notify_nightly_cap"
	KeyTitleChars = "notify_title_chars"
	KeyBodyChars  = "notify_body_chars"
)

func init() {
	settings.MustRegister(
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyPerRunCap, Module: module, Type: settings.Int, Default: "3",
			Check: settings.Between(0, 100)}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyNightlyCap, Module: module, Type: settings.Int, Default: "1",
			Check: settings.Between(0, 100)}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyTitleChars, Module: module, Type: settings.Int, Default: "120",
			Check: settings.Between(1, 250)}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyBodyChars, Module: module, Type: settings.Int, Default: "1000",
			Check: settings.Between(1, 4000)}),
	)
}

func Enabled(ctx context.Context) bool {
	return modules.IsEnabled(ctx, modules.Notifications)
}

func text(ctx context.Context, q db.Querier, key string) (string, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	return strings.TrimSpace(found.Value), err
}

func whole(ctx context.Context, q db.Querier, key string) (int, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	if err != nil {
		return 0, err
	}
	n, err := found.Int()
	return int(n), err
}
