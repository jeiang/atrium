package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/homecard"
)

const PageName application.Name = "weather.page"

type PageInput struct{}

type WeatherPage struct {
	State     homecard.State    `json:"state" api:"enum=off|unconfigured|loading|failed|stale|ok"`
	FetchedAt *float64          `json:"fetched_at"`
	Failure   *string           `json:"failure" api:"enum=unreachable|timeout|no_match|bad_reply|unavailable"`
	Location  *string           `json:"location"`
	Timezone  *string           `json:"timezone"`
	Current   *homecard.Current `json:"current"`
	Hours     []homecard.Hour   `json:"hours"`
	Days      []homecard.Day    `json:"days"`
}

func DescribePage() application.Definition {
	return application.Describe[PageInput, WeatherPage](PageName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) Page(ctx context.Context, now func() float64) (application.Read[WeatherPage], error) {
	l := readLatest(ctx, s.db, now)
	out := WeatherPage{State: l.state, Failure: l.failure, FetchedAt: l.fetchedAt, Hours: []homecard.Hour{}, Days: []homecard.Day{}}
	if f := l.forecast; f != nil {
		out.Location, out.Timezone, out.Current = &f.Location.Name, &f.Timezone, &f.Current
		out.Hours = append(out.Hours, f.Hourly...)
		out.Days = append(out.Days, f.Daily...)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return application.Read[WeatherPage]{}, err
	}
	return application.Read[WeatherPage]{Revision: application.Revision(fmt.Sprintf("%x", sha256.Sum256(raw))), Data: out}, nil
}
