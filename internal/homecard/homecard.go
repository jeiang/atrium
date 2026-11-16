package homecard

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

type State string

const (
	StateOff          State = "off"
	StateUnconfigured State = "unconfigured"
	StateLoading      State = "loading"
	StateFailed       State = "failed"
	StateStale        State = "stale"
	StateOK           State = "ok"
)

type Current struct {
	Time          string  `json:"time"`
	Temperature   float64 `json:"temperature_c"`
	Apparent      float64 `json:"apparent_c"`
	Humidity      float64 `json:"humidity"`
	Precipitation float64 `json:"precipitation_mm"`
	WeatherCode   int     `json:"weather_code"`
	Condition     string  `json:"condition"`
	WindSpeed     float64 `json:"wind_kmh"`
	WindDirection float64 `json:"wind_direction"`
	IsDay         bool    `json:"is_day"`
}

type Hour struct {
	Time          string  `json:"time"`
	Temperature   float64 `json:"temperature_c"`
	Precipitation float64 `json:"precipitation_probability"`
	WeatherCode   int     `json:"weather_code"`
	Condition     string  `json:"condition"`
}

type Day struct {
	Date          string  `json:"date"`
	High          float64 `json:"high_c"`
	Low           float64 `json:"low_c"`
	Precipitation float64 `json:"precipitation_probability"`
	WeatherCode   int     `json:"weather_code"`
	Condition     string  `json:"condition"`
	Sunrise       string  `json:"sunrise,omitempty"`
	Sunset        string  `json:"sunset,omitempty"`
}

type Weather struct {
	State     State    `json:"state" api:"enum=off|unconfigured|loading|failed|stale|ok"`
	FetchedAt *float64 `json:"fetched_at"`
	Failure   *string  `json:"failure" api:"enum=unreachable|timeout|no_match|bad_reply|unavailable"`
	Location  *string  `json:"location"`
	Timezone  *string  `json:"timezone"`
	Current   *Current `json:"current"`
	Today     *Day     `json:"today"`
	Hours     []Hour   `json:"hours"`
}

type WeatherCard interface {
	Weather(ctx context.Context, d *db.DB, now func() float64) Weather
}

func Off() Weather {
	return Weather{State: StateOff, Hours: []Hour{}}
}
