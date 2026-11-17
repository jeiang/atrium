package weathertools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	weatherapp "github.com/jeiang/atrium/internal/apps/weather/application"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "weather"

var inputSchema = json.RawMessage(`{"type":"object","properties":{"place":{"type":"string"},"days":{"type":"integer"},"hours":{"type":"integer"}},"additionalProperties":false}`)

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name: Name,
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
	place, _, err := tool.TextArg(args, "place", false)
	if err != nil {
		return nil, err
	}
	days, err := tool.BoundedInt(args, "days", weatherapp.ForecastDays, 16, 1)
	if err != nil {
		return nil, err
	}
	hours, err := tool.BoundedInt(args, "hours", weatherapp.HourlyHours, 48, 1)
	if err != nil {
		return nil, err
	}
	forecast, err := weatherapp.New(tc.DB).Forecast(ctx, weatherapp.ForecastInput{Place: place, Days: int(days), Hours: int(hours)})
	if err != nil {
		return nil, toolError(err)
	}
	return forecast.Data, nil
}

func toolError(err error) error {
	var e *application.Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Code {
	case application.NotConfigured:
		return tool.NotConfigured(e.Message)
	case application.Unavailable:
		return tool.Error(e.Message)
	}
	return err
}
