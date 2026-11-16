package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/weather"
)

const (
	WeatherUnreachable = "unreachable"
	WeatherTimeout     = "timeout"
	WeatherNoMatch     = "no_match"
	WeatherBadReply    = "bad_reply"
	WeatherUnavailable = "unavailable"
)

type HomeWeather struct {
	State     weather.State    `json:"state" api:"enum=off|unconfigured|loading|failed|stale|ok"`
	FetchedAt *float64         `json:"fetched_at"`
	Failure   *string          `json:"failure" api:"enum=unreachable|timeout|no_match|bad_reply|unavailable"`
	Location  *string          `json:"location"`
	Timezone  *string          `json:"timezone"`
	Current   *weather.Current `json:"current"`
	Today     *weather.Day     `json:"today"`
	Hours     []weather.Hour   `json:"hours"`
}

func (s *Service) HomeWeather(ctx context.Context) HomeWeather {
	found, err := weather.Load(ctx, s.db.Read)
	var notConfigured config.NotConfigured
	var disabled connections.DisabledConnection
	switch {
	case errors.As(err, &notConfigured), errors.As(err, &disabled):
		return HomeWeather{State: weather.StateOff, Hours: []weather.Hour{}}
	case err != nil:
		log.Warn("home weather settings unreadable", "err", err.Error())
		failure := WeatherUnavailable
		return HomeWeather{State: weather.StateFailed, Failure: &failure, Hours: []weather.Hour{}}
	case found.ForecastOff:
		return HomeWeather{State: weather.StateOff, Hours: []weather.Hour{}}
	case !found.HasLocation && found.LocationName == "":
		return HomeWeather{State: weather.StateUnconfigured, Hours: []weather.Hour{}}
	}
	key := fmt.Sprintf("%s|%t|%g|%g|%s|%s", found.LocationName, found.HasLocation, found.Latitude, found.Longitude, found.ForecastURL, found.GeocodingURL)
	last := weather.LastFor(s.db)
	reading, due := last.Read(key, s.wallClock())
	if due {
		go s.refreshHomeWeather(context.WithoutCancel(ctx), last, key, found)
	}
	out := HomeWeather{State: reading.State, Hours: []weather.Hour{}}
	if reading.Failure != "" {
		out.Failure = &reading.Failure
	}
	if f := reading.Forecast; f != nil {
		at := float64(reading.FetchedAt.UnixMicro()) / 1e6
		out.FetchedAt, out.Location, out.Timezone, out.Current = &at, &f.Location.Name, &f.Timezone, &f.Current
		out.Hours = append(out.Hours, f.Hourly...)
		if len(f.Daily) > 0 {
			out.Today = &f.Daily[0]
		}
	}
	return out
}

func (s *Service) wallClock() time.Time {
	return time.UnixMicro(int64(s.clock()() * 1e6))
}

func (s *Service) refreshHomeWeather(ctx context.Context, last *weather.Last, key string, found weather.Settings) {
	fetchCtx, cancel := context.WithTimeout(ctx, weather.TuningWeatherTimeoutSeconds*time.Second)
	defer cancel()
	forecast, failure := fetchHomeWeather(fetchCtx, found)
	if failure != "" {
		log.Warn("home weather refresh failed", "failure", failure)
	}
	if !last.Done(key, forecast, failure, s.wallClock()) {
		return
	}
	if err := s.state.Touch(ctx, state.Assistant); err != nil {
		log.Warn("home weather event not published", "err", err.Error())
	}
}

func fetchHomeWeather(ctx context.Context, found weather.Settings) (*weather.Forecast, string) {
	provider := weather.NewOpenMeteo(found.HTTP, found.ForecastURL, found.GeocodingURL)
	location := weather.Location{Name: found.LocationName, Latitude: found.Latitude, Longitude: found.Longitude}
	classify := func(err error) string {
		switch {
		case ctx.Err() != nil:
			return WeatherTimeout
		case errors.Is(err, weather.ErrUnreachable):
			return WeatherUnreachable
		default:
			return WeatherBadReply
		}
	}
	if !found.HasLocation {
		matches, err := provider.Geocode(ctx, found.LocationName)
		if err != nil {
			return nil, classify(err)
		}
		if len(matches) == 0 {
			return nil, WeatherNoMatch
		}
		location = matches[0]
	}
	forecast, err := provider.Forecast(ctx, location, 1, weather.TuningWeatherHourlyHours)
	if err != nil {
		return nil, classify(err)
	}
	return &forecast, ""
}
