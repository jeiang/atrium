package librarymodule

import (
	"context"

	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func Consumer() contract.Consumer {
	return contract.Consumer{
		Label:    library.TagMedia,
		Consumed: library.TagFiled,
		Skipped:  library.TagIgnored,
		Title:    "the library",
		Added:    selectAdded,
	}
}

func selectAdded(ctx context.Context, d contract.Deps, in contract.Intake, a contract.Addition) error {
	if !module.IsEnabled(ctx, unit.Media) {
		return nil
	}
	if err := library.RecordRequest(ctx, d.DB.Write, a.Hash, a.Scope, library.Now()); err != nil {
		return err
	}
	set, err := library.Load(ctx, d.DB.Read, d.Env)
	if err != nil {
		return err
	}
	set.Intake = in
	_, err = set.SelectFiles(ctx, set, a.Scope, nil)
	return err
}
