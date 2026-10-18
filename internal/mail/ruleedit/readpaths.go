package ruleedit

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "ruleedit.LearnedRulesSummary icloud",
		Golden: "ruleedit.learned_rules_summary icloud",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return LearnedRulesSummary(ctx, d.Read, "icloud", DefaultLearnedLimit)
		},
	})
}
