package calendar

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/timex"
	"golang.org/x/oauth2"
	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

func formBody(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = url.QueryEscape(p[0]) + "=" + url.QueryEscape(p[1])
	}
	return strings.Join(parts, "&")
}

type leaseToken struct {
	ctx        context.Context
	lease      *connections.Lease
	capability string
}

func (l leaseToken) Token() (*oauth2.Token, error) {
	value, err := l.lease.Token(l.ctx, l.capability)
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: value, TokenType: "Bearer"}, nil
}

func googleService(ctx context.Context, env config.Env, set Settings) (*gcal.Service, error) {
	if set.Google == nil {
		return nil, NotConfigured("google/default is not configured")
	}
	base, err := set.Google.Transport()
	if err != nil {
		return nil, err
	}
	round := base.Transport
	if round == nil {
		round = http.DefaultTransport
	}
	source := oauth2.ReuseTokenSource(nil, leaseToken{ctx: ctx, lease: set.Google, capability: connections.GoogleCalendar})
	client := &http.Client{
		Timeout:   remoteTimeout,
		Transport: &oauth2.Transport{Source: source, Base: acceptJSON{round}},
	}
	opts := []option.ClientOption{option.WithHTTPClient(client)}
	if api := EndpointsFrom(env).GoogleAPI; api != "" {
		opts = append(opts, option.WithEndpoint(api))
	}
	return gcal.NewService(ctx, opts...)
}

func googleEvents(ctx context.Context, env config.Env, set Settings, ws, we time.Time, tz *time.Location) ([]Row, error) {
	svc, err := googleService(ctx, env, set)
	if err != nil {
		return nil, err
	}
	out, token := []Row{}, ""
	for len(out) < set.MaxEvents*2 {
		call := svc.Events.List("primary").TimeMin(isoAuto(ws)).TimeMax(isoAuto(we)).SingleEvents(true).
			OrderBy("startTime").MaxResults(250).Context(ctx)
		if token != "" {
			call = call.PageToken(token)
		}
		reply, err := call.Do()
		if err != nil {
			return nil, remote(err)
		}
		for _, item := range reply.Items {
			if item.Status == "cancelled" {
				continue
			}
			var s, e struct{ dateTime, date string }
			if item.Start != nil {
				s.dateTime, s.date = item.Start.DateTime, item.Start.Date
			}
			if item.End != nil {
				e.dateTime, e.date = item.End.DateTime, item.End.Date
			}
			start, err := googleTime(s.dateTime, s.date, tz)
			if err != nil {
				return nil, err
			}
			end, err := googleTime(e.dateTime, e.date, tz)
			if err != nil {
				return nil, err
			}
			out = append(out, Row{UID: item.Id, Calendar: Google, Start: start, End: end, Title: item.Summary, Location: item.Location})
		}
		token = reply.NextPageToken
		if token == "" {
			break
		}
	}
	return out, nil
}

func googleTime(dateTime, date string, tz *time.Location) (Value, error) {
	if dateTime == "" {
		stamp, ok := parseISO(date)
		if !ok {
			return Value{}, valueError("invalid ISO-8601 date")
		}
		y, m, d := stamp.t.Date()
		return dateValue(y, m, d), nil
	}
	stamp, ok := parseISO(dateTime)
	if !ok {
		return Value{}, valueError("invalid ISO-8601 date/time")
	}
	instant := time.Unix(timex.WallToUnix(tz, stamp.t.Unix()), 0)
	if stamp.aware {
		instant = time.Unix(stamp.t.Unix(), 0)
	}
	return awareValue(instant.In(tz)), nil
}
