package mediamodule

import (
	"context"

	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func Consumer() contract.Consumer {
	return contract.Consumer{
		Label:    media.TagMedia,
		Consumed: media.TagFiled,
		Skipped:  media.TagIgnored,
		Title:    "the library",
		Added:    selectAdded,
	}
}

func selectAdded(ctx context.Context, d contract.Deps, in contract.Intake, scope contract.Scope) error {
	if !module.IsEnabled(ctx, unit.Media) {
		return nil
	}
	set, err := media.Load(ctx, d.DB.Read, d.Env)
	if err != nil {
		return err
	}
	set.Intake = in
	_, err = set.SelectFiles(ctx, set, scope, nil)
	return err
}
