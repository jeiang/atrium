package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/weather"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/homecard"
)

const (
	Unreachable = "unreachable"
	Timeout     = "timeout"
	NoMatch     = "no_match"
	BadReply    = "bad_reply"
	Unavailable = "unavailable"
)

type Card struct{}

func (Card) Weather(ctx context.Context, d *db.DB, now func() float64) homecard.Weather {
	wallClock := func() time.Time { return time.UnixMicro(int64(now() * 1e6)) }
	found, err := weather.Load(ctx, d.Read)
	var notConfigured config.NotConfigured
	var disabled connections.DisabledConnection
	switch {
	case errors.As(err, &notConfigured), errors.As(err, &disabled):
		return homecard.Off()
	case err != nil:
		log.Warn("home weather settings unreadable", "err", err.Error())
		failure := Unavailable
		return homecard.Weather{State: homecard.StateFailed, Failure: &failure, Hours: []homecard.Hour{}}
	case found.ForecastOff:
		return homecard.Off()
	case !found.HasLocation && found.LocationName == "":
		return homecard.Weather{State: homecard.StateUnconfigured, Hours: []homecard.Hour{}}
	}
	key := fmt.Sprintf("%s|%t|%g|%g|%s|%s", found.LocationName, found.HasLocation, found.Latitude, found.Longitude, found.ForecastURL, found.GeocodingURL)
	last := weather.LastFor(d)
	reading, due := last.Read(key, wallClock())
	if due {
		go refresh(context.WithoutCancel(ctx), state.New(d), last, key, found, wallClock)
	}
	out := homecard.Weather{State: reading.State, Hours: []homecard.Hour{}}
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

func refresh(ctx context.Context, events *state.Store, last *weather.Last, key string, found weather.Settings, wallClock func() time.Time) {
	fetchCtx, cancel := context.WithTimeout(ctx, weather.TuningWeatherTimeoutSeconds*time.Second)
	defer cancel()
	forecast, failure := fetchHomeWeather(fetchCtx, found)
	if failure != "" {
		log.Warn("home weather refresh failed", "failure", failure)
	}
	if !last.Done(key, forecast, failure, wallClock()) {
		return
	}
	if err := events.Touch(ctx, state.Assistant); err != nil {
		log.Warn("home weather event not published", "err", err.Error())
	}
}

func fetchHomeWeather(ctx context.Context, found weather.Settings) (*weather.Forecast, string) {
	provider := weather.NewOpenMeteo(found.HTTP, found.ForecastURL, found.GeocodingURL)
	location := weather.Location{Name: found.LocationName, Latitude: found.Latitude, Longitude: found.Longitude}
	classify := func(err error) string {
		switch {
		case ctx.Err() != nil:
			return Timeout
		case errors.Is(err, weather.ErrUnreachable):
			return Unreachable
		default:
			return BadReply
		}
	}
	if !found.HasLocation {
		matches, err := provider.Geocode(ctx, found.LocationName)
		if err != nil {
			return nil, classify(err)
		}
		if len(matches) == 0 {
			return nil, NoMatch
		}
		location = matches[0]
	}
	forecast, err := provider.Forecast(ctx, location, 1, weather.TuningWeatherHourlyHours)
	if err != nil {
		return nil, classify(err)
	}
	return &forecast, ""
}
