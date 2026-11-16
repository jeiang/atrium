package application

import (
	"context"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/weather"
)

type HomeWeather struct {
	Location string          `json:"location"`
	Timezone string          `json:"timezone"`
	Current  weather.Current `json:"current"`
	Today    *weather.Day    `json:"today"`
	Hours    []weather.Hour  `json:"hours"`
}

func (s *Service) HomeWeather(ctx context.Context) *HomeWeather {
	ctx, cancel := context.WithTimeout(ctx, weather.TuningHomeWeatherSeconds*time.Second)
	defer cancel()
	read, err := s.Weather(ctx, WeatherInput{Days: 1, Hours: weather.TuningWeatherHourlyHours})
	var appErr *application.Error
	var notConfigured config.NotConfigured
	switch {
	case errors.As(err, &notConfigured), errors.As(err, &appErr) && appErr.Code == application.NotConfigured:
		return nil
	case err != nil:
		log.Warn("home weather unavailable", "err", err.Error())
		return nil
	}
	f := read.Data
	out := &HomeWeather{Location: f.Location.Name, Timezone: f.Timezone, Current: f.Current, Hours: append([]weather.Hour{}, f.Hourly...)}
	if len(f.Daily) > 0 {
		out.Today = &f.Daily[0]
	}
	return out
}
