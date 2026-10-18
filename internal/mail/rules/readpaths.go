package rules

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "rules.ExportRules",
		Golden: "rules.export_rules",
		Run:    func(ctx context.Context, d *db.DB) (any, error) { return ExportRules(ctx, d.Read) },
	})
}
