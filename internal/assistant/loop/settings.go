package loop

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/db"
)

func readWebSearchMaxUses(_ context.Context, _ db.Querier) (int, error) {
	return assistant.TuningAgentWebSearchMaxUses, nil
}

func readWebSearchDynamicFiltering(_ context.Context, _ db.Querier) (bool, error) {
	return registry.TuningSearchWebDynamicFiltering, nil
}
