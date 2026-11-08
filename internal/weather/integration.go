package weather

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/integrations"
)

func init() {
	integrations.Register(integrations.Integration{
		Name: "open_meteo", Label: "Open-Meteo", Module: Module,
		Help: "Public weather forecasts and place lookup; no key.",
		Test: testOpenMeteo,
	})
}

func testOpenMeteo(ctx context.Context, c integrations.Conn) (string, error) {
	found, err := Load(ctx, c.DB.Read)
	if err != nil {
		return "", err
	}
	location := Location{Latitude: 0, Longitude: 0}
	if found.HasLocation {
		location = Location{Name: found.LocationName, Latitude: found.Latitude, Longitude: found.Longitude}
	}
	forecast, err := NewOpenMeteo(c.HTTP, found.ForecastURL, found.GeocodingURL).Forecast(ctx, location, 1, 1)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("forecast available (%s)", forecast.Current.Condition), nil
}
