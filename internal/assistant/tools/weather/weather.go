package weathertools

import (
	"context"
	"encoding/json"
	"strings"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

var inputSchema = json.RawMessage(`{"type":"object","properties":{"place":{"type":"string"},"days":{"type":"integer"},"hours":{"type":"integer"}},"additionalProperties":false}`)

type Service func(*tool.Context) *asstapp.Service

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{{
		Name: "weather",
		Description: "Current conditions and hourly/daily forecast for the configured location, or for a named place. " +
			"Open-Meteo data; temperatures are Celsius, wind km/h and precipitation mm.",
		InputSchema: inputSchema,
		Handler:     handle(svc),
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

func handle(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		place, _, err := tool.TextArg(args, "place", false)
		if err != nil {
			return nil, err
		}
		days, err := tool.BoundedInt(args, "days", asstapp.WeatherForecastDays, 16, 1)
		if err != nil {
			return nil, err
		}
		hours, err := tool.BoundedInt(args, "hours", asstapp.WeatherHourlyHours, 48, 1)
		if err != nil {
			return nil, err
		}
		forecast, err := svc(tc).Weather(ctx, asstapp.WeatherInput{Place: place, Days: int(days), Hours: int(hours)})
		if err != nil {
			return nil, asstapp.ToolError(err)
		}
		return forecast.Data, nil
	}
}
