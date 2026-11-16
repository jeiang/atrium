package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/weather"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.weather.application")

const ForecastName application.Name = "weather.forecast"

const (
	ForecastDays = weather.TuningWeatherForecastDays
	HourlyHours  = weather.TuningWeatherHourlyHours
)

type ForecastInput struct {
	Place string
	Days  int
	Hours int
}

func DescribeForecast() application.Definition {
	return application.Describe[ForecastInput, weather.Forecast](ForecastName, application.Query,
		[]application.Authority{application.Agent, application.Approved}, application.NoEffect, false)
}

type Service struct {
	db *db.DB
}

func New(d *db.DB) *Service {
	return &Service{db: d}
}

func (s *Service) Forecast(ctx context.Context, in ForecastInput) (application.Read[weather.Forecast], error) {
	fail := func(err error) (application.Read[weather.Forecast], error) {
		return application.Read[weather.Forecast]{}, err
	}
	found, err := weather.Load(ctx, s.db.Read)
	if err != nil {
		return fail(err)
	}
	provider := weather.NewOpenMeteo(found.HTTP, found.ForecastURL, found.GeocodingURL)
	location := weather.Location{Name: found.LocationName, Latitude: found.Latitude, Longitude: found.Longitude}
	place := in.Place
	if place == "" && !found.HasLocation {
		if found.LocationName == "" {
			return fail(&application.Error{Code: application.NotConfigured, Message: "weather_latitude and weather_longitude are not set"})
		}
		place = found.LocationName
	}
	if place != "" {
		matches, err := provider.Geocode(ctx, place)
		if err != nil {
			return fail(unavailable(err.Error()))
		}
		if len(matches) == 0 {
			return fail(unavailable("no place matches " + place))
		}
		location = matches[0]
	}
	forecast, err := provider.Forecast(ctx, location, in.Days, in.Hours)
	if err != nil {
		return fail(unavailable(err.Error()))
	}
	return application.Read[weather.Forecast]{Data: forecast}, nil
}

func unavailable(message string) error {
	return &application.Error{Code: application.Unavailable, Message: message}
}
