package readpaths_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/mail/report"
	"github.com/jeiang/atrium/internal/mail/search"
	"github.com/jeiang/atrium/internal/mail/triage"
)

// goldenNow is the clock make_db.py's last scenario step left running (manifest.json clock.steps.scan2).
var goldenNow = time.Unix(1793392200, 0)

func goldenEnv() config.Env {
	return config.Map(map[string]string{"ATRIUM_TZ": "America/Port_of_Spain"})
}

// sparseEmbedding is make_db.py's local model: a bag of sha256-bucketed tokens, normalised as embed.Texts does.
func sparseEmbedding(ctx context.Context, _ string, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, text := range texts {
		vector := make([]float64, config.EmbedDim)
		for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		}) {
			sum := sha256.Sum256([]byte(token))
			vector[binary.BigEndian.Uint32(sum[:4])%config.EmbedDim]++
		}
		out[i] = embed.Normalize(vector)
	}
	return out, nil
}

func init() {
	for _, c := range []struct {
		key     string
		query   string
		filters map[string]any
		limit   int
	}{
		{"search.search power bill", "power bill", nil, search.DefaultLimit},
		{"search.search receipt gmail", "receipt", map[string]any{"account": "gmail"}, search.DefaultLimit},
		{"search.search folder Receipts", "shop", map[string]any{"folder": "Receipts"}, 5},
	} {
		readpaths.Register(readpaths.Path{
			Name:   "mail." + c.key,
			Golden: c.key,
			Run: func(ctx context.Context, d *db.DB) (any, error) {
				return search.Search(ctx, d.Read, config.Config{}, c.query, c.filters, c.limit, sparseEmbedding)
			},
		})
	}
	since := "365d"
	readpaths.Register(readpaths.Path{
		Name:   "mail.report.build 365d",
		Golden: "report.build 365d",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return report.Build(ctx, d.Read, goldenEnv(), &since, goldenNow)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "mail.triage.triage",
		Golden: "triage.triage",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return triage.Triage(ctx, d.Read, triage.Filter{})
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "mail.triage.triage icloud llm",
		Golden: "triage.triage icloud llm",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return triage.Triage(ctx, d.Read, triage.Filter{Account: "icloud", Stage: "llm"})
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "mail.triage.summaries",
		Golden: "triage.summaries",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return triage.Summaries(ctx, d.Read, goldenEnv(), nil, goldenNow)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "mail.triage.flag_judgments",
		Golden: "triage.flag_judgments",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return triage.FlagJudgments(ctx, d.Read, "", 50, 0)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "mail.triage.folders icloud",
		Golden: "triage.folders icloud",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return triage.Folders(ctx, d.Read, "icloud")
		},
	})
}
