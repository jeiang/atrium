package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

func memoryToolService(tc *tool.Context) *asstapp.Service {
	return asstapp.New(tc.DB, tc.Env, tc.Now)
}

func memoryCLIService(env config.Env, now func() float64) memorycmd.Open {
	if now == nil {
		now = assistant.Now
	}
	return func(ctx context.Context) (*asstapp.Service, func(), error) {
		cfg, err := config.Load(env)
		if err != nil {
			return nil, nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return nil, nil, err
		}
		if err := config.Boot(ctx, env, d, cfg); err != nil {
			_ = d.Close()
			return nil, nil, err
		}
		return asstapp.New(d, env, now), func() { _ = d.Close() }, nil
	}
}

func memoryOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeMemoryTree(), asstapp.DescribeMemoryView(), asstapp.DescribeMemoryEstimate(),
			asstapp.DescribeMemoryWork(), asstapp.DescribeMemoryBackfill(), asstapp.DescribeMemoryZoom(),
			asstapp.DescribeMemorySearch(), asstapp.DescribeResultRecall(), asstapp.DescribeUsage(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "memory tree", Uses: uses(asstapp.MemoryTreeName)},
			{Kind: application.CLI, Target: "memory view", Uses: uses(asstapp.MemoryViewName)},
			{Kind: application.CLI, Target: "memory work", Uses: uses(asstapp.MemoryWorkName)},
			{Kind: application.CLI, Target: "memory backfill", Uses: uses(asstapp.MemoryEstimateName, asstapp.MemoryBackfillName)},
			{Kind: application.Job, Target: module.Assistant + "/" + assistant.MemoryWorkName, Uses: uses(asstapp.MemoryWorkName)},
			{Kind: application.Tool, Target: "zoom", Uses: uses(asstapp.MemoryZoomName)},
			{Kind: application.Tool, Target: "search_threads", Uses: uses(asstapp.MemorySearchName)},
			{Kind: application.Tool, Target: "recall", Uses: uses(asstapp.ResultRecallName)},
			{Kind: application.HTML, Target: "GET /settings/usage", Uses: uses(asstapp.UsageName)},
		}
}
