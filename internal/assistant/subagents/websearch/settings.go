package websearch

import (
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyMaxCostUSD  = "search_web_max_cost_usd"
	KeyMaxSteps    = "search_web_max_steps"
	KeyMaxSearches = "search_web_max_searches"
	KeyModel       = "search_web_model"
)

func init() {
	model := settings.Value(settings.UI, settings.Def{
		Key: KeyModel, Module: "web_search", Type: settings.Enum, Default: pricing.DefaultModel,
	})
	model.Choices = pricing.IDs()
	settings.MustRegister(
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyMaxCostUSD, Module: "web_search", Type: settings.Float, Default: "0.15", Check: settings.Within(0, 2),
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyMaxSteps, Module: "web_search", Type: settings.Int, Default: "8", Check: settings.Between(1, 24),
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyMaxSearches, Module: "web_search", Type: settings.Int, Default: "5", Check: settings.Between(1, 20),
		}),
		model,
	)
}
