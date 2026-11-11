package weathertools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/weather"
)

var inputSchema = json.RawMessage(`{"type":"object","properties":{"place":{"type":"string"},"days":{"type":"integer"},"hours":{"type":"integer"}},"additionalProperties":false}`)

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name: "weather",
		Description: "Current conditions and hourly/daily forecast for the configured location, or for a named place. " +
			"Open-Meteo data; temperatures are Celsius, wind km/h and precipitation mm.",
		InputSchema: inputSchema,
		Handler:     handle,
		Effect:      tool.Read,
		Annotated:   true,
		Channel:     tool.ChannelNone,
		ChannelOf:   channelOf,
		Contexts:    tool.AllKinds,
	}}
}

func channelOf(args tool.Args) tool.Channel {
	if place, ok := args.Get("place"); ok {
		if text, isText := place.(string); isText && strings.TrimSpace(text) != "" {
			return tool.ChannelInternet
		}
	}
	return tool.ChannelNone
}

func handle(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	found, err := weather.Load(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	place, _, err := tool.TextArg(args, "place", false)
	if err != nil {
		return nil, err
	}
	days, err := tool.BoundedInt(args, "days", weather.TuningWeatherForecastDays, 16, 1)
	if err != nil {
		return nil, err
	}
	hours, err := tool.BoundedInt(args, "hours", weather.TuningWeatherHourlyHours, 48, 1)
	if err != nil {
		return nil, err
	}
	provider := weather.NewOpenMeteo(found.HTTP, found.ForecastURL, found.GeocodingURL)
	location := weather.Location{Name: found.LocationName, Latitude: found.Latitude, Longitude: found.Longitude}
	switch {
	case place != "":
		matches, err := provider.Geocode(ctx, place)
		if err != nil {
			return nil, tool.Error(err.Error())
		}
		if len(matches) == 0 {
			return nil, tool.Error("no place matches " + place)
		}
		location = matches[0]
	case !found.HasLocation:
		if found.LocationName == "" {
			return nil, tool.NotConfigured("weather_latitude and weather_longitude are not set")
		}
		matches, err := provider.Geocode(ctx, found.LocationName)
		if err != nil {
			return nil, tool.Error(err.Error())
		}
		if len(matches) == 0 {
			return nil, tool.Error("no place matches " + found.LocationName)
		}
		location = matches[0]
	}
	forecast, err := provider.Forecast(ctx, location, int(days), int(hours))
	if err != nil {
		return nil, tool.Error(err.Error())
	}
	return forecast, nil
}
