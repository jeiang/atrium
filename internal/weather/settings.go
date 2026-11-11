package weather

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
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
	settings.MustRegister(
		ui(KeyLocationName, settings.String, "", nil),
		ui(KeyLatitude, settings.Float, "", settings.Between(-90, 90)),
		ui(KeyLongitude, settings.Float, "", settings.Between(-180, 180)),
		settings.Deploy(settings.Def{Key: KeyForecastURL, Module: Module, Type: settings.String, Default: DefaultForecastURL, Check: httpURL}),
		settings.Deploy(settings.Def{Key: KeyGeocodingURL, Module: Module, Type: settings.String, Default: DefaultGeocodingURL, Check: httpURL}),
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
	ForecastURL  string
	GeocodingURL string
	HTTP         *http.Client
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
	lease, err := connections.OpenRead(ctx, q, config.From(ctx).Host(), connections.ID(KindWeather))
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return Settings{}, config.NotConfigured("the weather connection is not configured")
		}
		return Settings{}, err
	}
	if s.ForecastURL, err = lease.Endpoint("url"); err != nil {
		return Settings{}, err
	}
	var cfg connectionConfig
	if err := json.Unmarshal(lease.Config(), &cfg); err != nil {
		return Settings{}, err
	}
	s.GeocodingURL = strings.TrimSpace(cfg.GeocodingURL)
	if s.HTTP, err = lease.HTTP("url"); err != nil {
		return Settings{}, err
	}
	return s, nil
}
