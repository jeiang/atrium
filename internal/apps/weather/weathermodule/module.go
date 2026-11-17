package weathermodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/weather"
	weatherapp "github.com/jeiang/atrium/internal/apps/weather/application"
	"github.com/jeiang/atrium/internal/apps/weather/weathertools"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

type Module struct{}

func (Module) Declare(module.Context) module.Declaration {
	tools := make([]module.ToolDefinition, 0, 1)
	for _, t := range weathertools.Tools() {
		tools = append(tools, module.ToolDefinition{Tool: t})
	}
	return module.Declaration{
		Name:     unit.Weather,
		Title:    "Weather",
		App:      true,
		Inherits: unit.Assistant,
		Needs: []module.Need{
			{Kind: weather.KindWeather, Soft: true, Capability: "forecast", Features: []string{"weather"}},
			{Kind: weather.KindWeather, Soft: true, Capability: "geocoding", Features: []string{"weather place lookup"}},
		},
		Packages:    []string{"internal/apps/weather"},
		Settings:    weather.SettingsDefs(),
		Groups:      weather.SettingGroups(),
		Tools:       tools,
		Connections: weather.Connections(),
		HomeWeather: weatherapp.Card{},
		Operations:  []application.Definition{weatherapp.DescribeForecast()},
		Bindings:    []application.Binding{{Kind: application.Tool, Target: weathertools.Name, Uses: []application.Name{weatherapp.ForecastName}}},
	}
}
