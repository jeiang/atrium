package weather

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const Module = "assistant"

const (
	KeyLocationName = "weather_location_name"
	KeyLatitude     = "weather_latitude"
	KeyLongitude    = "weather_longitude"
	KeyForecastDays = "weather_forecast_days"
	KeyHourlyHours  = "weather_hourly_hours"
	KeyTimeout      = "weather_timeout_seconds"
	KeyForecastURL  = "weather_open_meteo_url"
	KeyGeocodingURL = "weather_geocoding_url"
)

const (
	DefaultForecastURL  = "https://api.open-meteo.com/v1/forecast"
	DefaultGeocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
)

func init() {
	ui := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.UI, settings.Def{Key: key, Module: Module, Type: kind, Default: def, Check: check})
	}
	adv := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.Advanced, settings.Def{Key: key, Module: Module, Type: kind, Default: def, Check: check})
	}
	settings.MustRegister(
		ui(KeyLocationName, settings.String, "", nil),
		ui(KeyLatitude, settings.Float, "", settings.Between(-90, 90)),
		ui(KeyLongitude, settings.Float, "", settings.Between(-180, 180)),
		adv(KeyForecastDays, settings.Int, "7", settings.Between(1, 16)),
		adv(KeyHourlyHours, settings.Int, "24", settings.Between(1, 48)),
		adv(KeyTimeout, settings.Int, "15", settings.Between(1, 300)),
		adv(KeyForecastURL, settings.String, DefaultForecastURL, httpURL),
		adv(KeyGeocodingURL, settings.String, DefaultGeocodingURL, httpURL),
	)
}

func httpURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return settings.Invalid("must be an http or https URL")
	}
	return nil
}

type Settings struct {
	LocationName string
	Latitude     float64
	Longitude    float64
	HasLocation  bool
	ForecastDays int
	HourlyHours  int
	Timeout      int
	ForecastURL  string
	GeocodingURL string
}

func Load(ctx context.Context, q db.Querier) (Settings, error) {
	read := func(key string) (string, error) {
		found, err := settings.From(ctx).Resolve(ctx, q, key)
		return strings.TrimSpace(found.Value), err
	}
	var s Settings
	var err error
	if s.LocationName, err = read(KeyLocationName); err != nil {
		return Settings{}, err
	}
	lat, err := read(KeyLatitude)
	if err != nil {
		return Settings{}, err
	}
	lon, err := read(KeyLongitude)
	if err != nil {
		return Settings{}, err
	}
	if lat != "" && lon != "" {
		if s.Latitude, err = strconv.ParseFloat(lat, 64); err != nil {
			return Settings{}, err
		}
		if s.Longitude, err = strconv.ParseFloat(lon, 64); err != nil {
			return Settings{}, err
		}
		s.HasLocation = true
	}
	days, err := read(KeyForecastDays)
	if err != nil {
		return Settings{}, err
	}
	if s.ForecastDays, err = strconv.Atoi(days); err != nil {
		return Settings{}, err
	}
	hours, err := read(KeyHourlyHours)
	if err != nil {
		return Settings{}, err
	}
	if s.HourlyHours, err = strconv.Atoi(hours); err != nil {
		return Settings{}, err
	}
	timeout, err := read(KeyTimeout)
	if err != nil {
		return Settings{}, err
	}
	if s.Timeout, err = strconv.Atoi(timeout); err != nil {
		return Settings{}, err
	}
	if s.ForecastURL, err = read(KeyForecastURL); err != nil {
		return Settings{}, err
	}
	if s.GeocodingURL, err = read(KeyGeocodingURL); err != nil {
		return Settings{}, err
	}
	return s, nil
}
