package calendar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
	"golang.org/x/oauth2"
)

type refresher struct {
	ctx      context.Context
	creds    config.GoogleCredentials
	tokenURL string
}

func formBody(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = url.QueryEscape(p[0]) + "=" + url.QueryEscape(p[1])
	}
	return strings.Join(parts, "&")
}

func (r *refresher) Token() (*oauth2.Token, error) {
	body := formBody([][2]string{
		{"grant_type", "refresh_token"}, {"client_id", r.creds.ID}, {"client_secret", r.creds.Secret},
		{"refresh_token", r.creds.RefreshToken}, {"scope", Scope},
	})
	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, r.tokenURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: remoteTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var reply struct {
		AccessToken string  `json:"access_token"`
		TokenType   string  `json:"token_type"`
		ExpiresIn   float64 `json:"expires_in"`
		Error       string  `json:"error"`
		Description string  `json:"error_description"`
	}
	_ = json.Unmarshal(data, &reply)
	if resp.StatusCode != http.StatusOK || reply.AccessToken == "" {
		return nil, &oauth2.RetrieveError{Response: resp, Body: data, ErrorCode: reply.Error, ErrorDescription: reply.Description}
	}
	token := &oauth2.Token{AccessToken: reply.AccessToken, TokenType: reply.TokenType}
	if reply.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(reply.ExpiresIn) * time.Second)
	}
	return token, nil
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

func googleTime(dateTime, date string, tz *time.Location) (string, error) {
	if dateTime == "" {
		return date, nil
	}
	dt, ok := py.FromISOFormat(dateTime)
	if !ok {
		return "", valueError(py.ISOFormatError(dateTime))
	}
	w := time.Date(dt.Year, time.Month(dt.Month), dt.Day, dt.Hour, dt.Minute, dt.Second, dt.Microsecond*1000, time.UTC)
	instant := time.Unix(py.WallToUnix(time.Local, w.Unix()), 0)
	if dt.Aware {
		instant = time.Unix(w.Unix()-int64(dt.OffsetSeconds), 0)
	}
	return py.ISOFormat(instant.In(tz), "seconds"), nil
}
