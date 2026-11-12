package websearch

import (
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyMaxCostUSD = "search_web_max_cost_usd"
	KeyModel      = "search_web_model"
)

const (
	TuningSearchWebMaxSteps    = 8
	TuningSearchWebMaxSearches = 5
)

func Settings() []settings.Def {
	model := settings.Value(settings.UI, settings.Def{
		Key: KeyModel, Label: "Web search model", Module: "web_search", Type: settings.Enum, Default: pricing.DefaultModel,
	})
	model.Choices = pricing.IDs()
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{
			Key: KeyMaxCostUSD, Label: "Web search run cost cap (USD)", Module: "web_search", Type: settings.Float, Default: "0.15", Check: settings.Within(0, 2),
		}),
		model,
	}
}
