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

type latest struct {
	state     homecard.State
	failure   *string
	fetchedAt *float64
	forecast  *weather.Forecast
}

func (Card) Weather(ctx context.Context, d *db.DB, now func() float64) homecard.Weather {
	l := readLatest(ctx, d, now)
	out := homecard.Weather{State: l.state, Failure: l.failure, FetchedAt: l.fetchedAt, Hours: []homecard.Hour{}}
	if f := l.forecast; f != nil {
		out.Location, out.Timezone, out.Current = &f.Location.Name, &f.Timezone, &f.Current
		out.Hours = append(out.Hours, f.Hourly...)
		if len(f.Daily) > 0 {
			out.Today = &f.Daily[0]
		}
	}
	return out
}

func readLatest(ctx context.Context, d *db.DB, now func() float64) latest {
	wallClock := func() time.Time { return time.UnixMicro(int64(now() * 1e6)) }
	found, err := weather.Load(ctx, d.Read)
	var notConfigured config.NotConfigured
	var disabled connections.DisabledConnection
	switch {
	case errors.As(err, &notConfigured), errors.As(err, &disabled):
		return latest{state: homecard.StateOff}
	case err != nil:
		log.Warn("home weather settings unreadable", "err", err.Error())
		failure := Unavailable
		return latest{state: homecard.StateFailed, failure: &failure}
	case found.ForecastOff:
		return latest{state: homecard.StateOff}
	case !found.HasLocation && found.LocationName == "":
		return latest{state: homecard.StateUnconfigured}
	}
	key := fmt.Sprintf("%s|%t|%g|%g|%s|%s", found.LocationName, found.HasLocation, found.Latitude, found.Longitude, found.ForecastURL, found.GeocodingURL)
	last := weather.LastFor(d)
	reading, due := last.Read(key, wallClock())
	if due {
		go refresh(context.WithoutCancel(ctx), state.New(d), last, key, found, wallClock)
	}
	out := latest{state: reading.State, forecast: reading.Forecast}
	if reading.Failure != "" {
		out.failure = &reading.Failure
	}
	if reading.Forecast != nil {
		at := float64(reading.FetchedAt.UnixMicro()) / 1e6
		out.fetchedAt = &at
	}
	return out
}

func refresh(ctx context.Context, events *state.Store, last *weather.Last, key string, found weather.Settings, wallClock func() time.Time) {
	fetchCtx, cancel := context.WithTimeout(ctx, weather.TuningWeatherTimeoutSeconds*time.Second)
	defer cancel()
	forecast, failure := fetchLatest(fetchCtx, found)
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

func fetchLatest(ctx context.Context, found weather.Settings) (*weather.Forecast, string) {
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
	forecast, err := provider.Forecast(ctx, location, weather.TuningWeatherForecastDays, weather.TuningWeatherHourlyHours)
	if err != nil {
		return nil, classify(err)
	}
	return &forecast, ""
}
