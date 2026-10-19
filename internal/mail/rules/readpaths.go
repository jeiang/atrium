package rules

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/py"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "rules.ExportRules",
		Golden: "rules.export_rules",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			doc, err := ExportRules(ctx, d.Read)
			return json.RawMessage(py.JSONDumps(doc)), err
		},
	})
}
