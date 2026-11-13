package application

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/textutil"
	"github.com/jeiang/atrium/internal/textx"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/weather"
)

type WebFetchInput struct {
	URL         string
	MaxChars    *int64
	AllowedURLs []string
}

type WebPage struct {
	URL       string
	Title     string
	Text      string
	Truncated bool
}

type WeatherInput struct {
	Place string
	Days  int
	Hours int
}

func DescribeWebFetch() application.Definition {
	return application.Describe[WebFetchInput, WebPage](WebFetchName, application.Query, agentCallers, application.NoEffect, false)
}

func DescribeWeather() application.Definition {
	return application.Describe[WeatherInput, weather.Forecast](WeatherName, application.Query, agentCallers, application.NoEffect, false)
}

func (s *Service) WebFetch(ctx context.Context, in WebFetchInput) (application.Read[WebPage], error) {
	url, err := webfetch.NormalizeURL(in.URL)
	if err != nil {
		return application.Read[WebPage]{}, err
	}
	lim, err := webfetch.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return application.Read[WebPage]{}, err
	}
	maxChars := int64(lim.TextDefault)
	if in.MaxChars != nil {
		maxChars = max(1, min(*in.MaxChars, maxChars))
	}
	allowed := make(map[string]struct{}, len(in.AllowedURLs))
	for _, u := range in.AllowedURLs {
		n, err := webfetch.NormalizeURL(u)
		if err != nil {
			continue
		}
		allowed[n] = struct{}{}
	}
	if _, ok := allowed[url]; !ok {
		return application.Read[WebPage]{}, tool.Error("url is not allowed: only URLs the operator typed or web_search returned in this thread can be fetched")
	}
	page, err := (&webfetch.Fetcher{}).FetchPage(ctx, url, lim)
	if err != nil {
		var te tool.Error
		if errors.As(err, &te) {
			return application.Read[WebPage]{}, err
		}
		return application.Read[WebPage]{}, tool.Error("fetch failed: " + err.Error())
	}
	markup := textutil.Decode(page.Body, page.Charset)
	title, body := webfetch.ExtractText(markup, page.URL, lim.PageChars)
	return application.Read[WebPage]{Data: WebPage{URL: page.URL, Title: title, Text: textx.Clip(body, int(maxChars)),
		Truncated: int64(utf8.RuneCountInString(body)) > maxChars}}, nil
}

func (s *Service) Weather(ctx context.Context, in WeatherInput) (application.Read[weather.Forecast], error) {
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
			return fail(childFailed(err.Error()))
		}
		if len(matches) == 0 {
			return fail(childFailed("no place matches " + place))
		}
		location = matches[0]
	}
	forecast, err := provider.Forecast(ctx, location, in.Days, in.Hours)
	if err != nil {
		return fail(childFailed(err.Error()))
	}
	return application.Read[weather.Forecast]{Data: forecast}, nil
}
