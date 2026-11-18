package weathermodule

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/weather"
	weatherapp "github.com/jeiang/atrium/internal/apps/weather/application"
	"github.com/jeiang/atrium/internal/apps/weather/weathertools"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
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
		Operations:  []application.Definition{weatherapp.DescribeForecast(), weatherapp.DescribePage()},
		Bindings: append([]application.Binding{
			{Kind: application.Tool, Target: weathertools.Name, Uses: []application.Name{weatherapp.ForecastName}},
		}, routes(nil)...),
		Pages: []web.Page{{
			Module: func(_ chi.Router, d *web.Deps) { routes(d) },
			Nav:    []web.NavItem{{Section: "apps", Label: "Weather", URL: "/apps/weather", Icon: "cloud", Page: "weather", Order: 10}},
		}},
	}
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func routes(d *web.Deps) []application.Binding {
	return []application.Binding{
		api.Query(d, weatherapp.DescribePage(), application.APIShape{Area: "integrations", Description: "The forecast for the configured weather location: current conditions, the next hours and the coming days, in degrees Celsius and km/h. Read-only; served from the same cached reading as the Home weather card, refreshed in the background."}, "GET /weather",
			func(ctx context.Context, _ struct{}) (application.Read[weatherapp.WeatherPage], error) {
				return weatherapp.New(d.DB).Page(ctx, now)
			}),
	}
}
