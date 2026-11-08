package calendar

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"golang.org/x/oauth2"
	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	ICloud        = "icloud"
	Google        = "google"
	CalDAVURL     = "https://caldav.icloud.com/"
	CardDAVURL    = "https://contacts.icloud.com/"
	TokenURI      = "https://oauth2.googleapis.com/token"
	Scope         = "https://www.googleapis.com/auth/calendar.events"
	remoteTimeout = 60 * time.Second
)

const TargetsText = "icloud, google"

type Target string

func ParseTarget(value string) (Target, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ICloud:
		return ICloud, nil
	case Google:
		return Google, nil
	}
	return "", Error("calendar must be one of " + TargetsText)
}

type Filter struct {
	Target     Target
	Collection string
}

func ParseFilter(value string) (Filter, error) {
	trimmed := strings.TrimSpace(value)
	switch {
	case trimmed == "":
		return Filter{}, nil
	case strings.EqualFold(trimmed, Google):
		return Filter{Target: Google}, nil
	case strings.EqualFold(trimmed, ICloud):
		return Filter{Target: ICloud}, nil
	}
	return Filter{Target: ICloud, Collection: trimmed}, nil
}

type Endpoints struct {
	Token     string
	GoogleAPI string
	CalDAV    string
	CardDAV   string
}

const (
	CalDAVEnv      = "ATRIUM_CALENDAR_CALDAV_URL"
	CardDAVEnv     = "ATRIUM_CALENDAR_CARDDAV_URL"
	GoogleTokenEnv = "ATRIUM_CALENDAR_GOOGLE_TOKEN_URL"
	GoogleAPIEnv   = "ATRIUM_CALENDAR_GOOGLE_API_URL"
)

func EndpointsFrom(env config.Env) Endpoints {
	pick := func(key, fallback string) string {
		if env != nil {
			if value, ok := env(key); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return fallback
	}
	return Endpoints{
		Token:     pick(GoogleTokenEnv, TokenURI),
		GoogleAPI: pick(GoogleAPIEnv, ""),
		CalDAV:    pick(CalDAVEnv, CalDAVURL),
		CardDAV:   pick(CardDAVEnv, CardDAVURL),
	}
}

type Ref struct {
	Calendar string
	UID      string
}

func (r Ref) Dict() py.Dict {
	return py.Dict{{Key: "calendar", Value: r.Calendar}, {Key: "uid", Value: r.UID}}
}

func Zone(ctx context.Context, env config.Env) (*time.Location, error) {
	return config.From(ctx).Location(env)
}

func targetError() error { return Error("calendar must be one of " + TargetsText) }

type acceptJSON struct{ base http.RoundTripper }

func (a acceptJSON) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Accept") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("Accept", "application/json")
	}
	return a.base.RoundTrip(r)
}

func googleService(ctx context.Context, env config.Env, set Settings) (*gcal.Service, error) {
	creds, err := googleCredentials(env, set)
	if err != nil {
		return nil, err
	}
	endpoints := EndpointsFrom(env)
	source := oauth2.ReuseTokenSource(nil, &refresher{ctx: ctx, creds: creds, tokenURL: endpoints.Token})
	client := &http.Client{
		Timeout:   remoteTimeout,
		Transport: &oauth2.Transport{Source: source, Base: acceptJSON{http.DefaultTransport}},
	}
	opts := []option.ClientOption{option.WithHTTPClient(client)}
	if endpoints.GoogleAPI != "" {
		opts = append(opts, option.WithEndpoint(endpoints.GoogleAPI))
	}
	return gcal.NewService(ctx, opts...)
}

func httpStatus(err error) int {
	var api *googleapi.Error
	if errors.As(err, &api) {
		return api.Code
	}
	return 0
}

func CreateEvent(ctx context.Context, env config.Env, set Settings, target Target, spec Spec, tz *time.Location) (Ref, error) {
	switch target {
	case Google:
		svc, err := googleService(ctx, env, set)
		if err != nil {
			return Ref{}, err
		}
		body, err := GoogleBody(spec, tz)
		if err != nil {
			return Ref{}, err
		}
		if _, err := svc.Events.Insert("primary", body).SendUpdates("none").Context(ctx).Do(); err != nil && httpStatus(err) != 409 {
			return Ref{}, remote(err)
		}
	case ICloud:
		cal, err := openICloud(ctx, env, set)
		if err != nil {
			return Ref{}, err
		}
		ics, err := buildCalendar(spec, tz, time.Now())
		if err != nil {
			return Ref{}, err
		}
		if err := cal.save(ctx, spec.UID, ics); err != nil {
			return Ref{}, remote(err)
		}
	default:
		return Ref{}, targetError()
	}
	return Ref{Calendar: string(target), UID: spec.UID}, nil
}

func DeleteEvent(ctx context.Context, env config.Env, set Settings, target Target, uid string) error {
	switch target {
	case Google:
		svc, err := googleService(ctx, env, set)
		if err != nil {
			return err
		}
		if err := svc.Events.Delete("primary", uid).SendUpdates("none").Context(ctx).Do(); err != nil {
			if status := httpStatus(err); status != 404 && status != 410 {
				return remote(err)
			}
		}
	case ICloud:
		cal, err := openICloud(ctx, env, set)
		if err != nil {
			return err
		}
		if err := cal.remove(ctx, uid); err != nil {
			return remote(err)
		}
	default:
		return targetError()
	}
	return nil
}

func Track(ctx context.Context, q db.Querier, spec Spec, target Target, proposalID *int64, now float64) error {
	encoded, err := json.Marshal(spec.Dict())
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		"INSERT OR REPLACE INTO agent_events (uid, calendar, title, start, end, spec, created_ts, deleted_ts, proposal_id) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)",
		spec.UID, string(target), spec.Title, spec.Start.iso(), spec.End.iso(), string(encoded), now, proposalID)
	return err
}

type Tracked struct {
	Calendar string
	Spec     string
}

func FindTracked(ctx context.Context, q db.Querier, uid string) (*Tracked, error) {
	var row Tracked
	err := q.QueryRowContext(ctx, "SELECT calendar, spec FROM agent_events WHERE uid = ? AND deleted_ts IS NULL", uid).Scan(&row.Calendar, &row.Spec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func Untrack(ctx context.Context, q db.Querier, uid string, now float64) error {
	_, err := q.ExecContext(ctx, "UPDATE agent_events SET deleted_ts = ? WHERE uid = ?", now, uid)
	return err
}

type Listing struct {
	Events    []Row
	Truncated bool
	Notes     []string
	tz        *time.Location
}

func (l Listing) Dict() py.Dict {
	events := make([]any, len(l.Events))
	for i, row := range l.Events {
		events[i] = row.Dict(l.tz)
	}
	notes := make([]any, len(l.Notes))
	for i, note := range l.Notes {
		notes[i] = note
	}
	return py.Dict{{Key: "events", Value: events}, {Key: "truncated", Value: l.Truncated}, {Key: "notes", Value: notes}}
}

func ListEvents(ctx context.Context, env config.Env, set Settings, start, end any, filter Filter) (*Listing, error) {
	tz, err := Zone(ctx, env)
	if err != nil {
		return nil, err
	}
	first, err := ParseWhen(start, tz, false)
	if err != nil {
		return nil, err
	}
	last, err := ParseWhen(end, tz, false)
	if err != nil {
		return nil, err
	}
	ws, we := asDT(first, tz), asDT(last, tz)
	if !we.After(ws) {
		return nil, Error("end must be after start")
	}
	events, notes := []Row{}, []string{}
	reached := 0
	var googleErr error
	if filter.Target != Google {
		if dir := set.CalendarDir; dir != "" {
			found, extra, err := MirrorEvents(dir, ws, we, tz, filter.Collection, set.MaxOccurrences)
			var missing NotConfigured
			switch {
			case errors.As(err, &missing):
				notes = append(notes, missing.Error())
			case err != nil:
				return nil, err
			default:
				events = append(events, found...)
				notes = append(notes, extra...)
				reached++
			}
		} else {
			notes = append(notes, "ATRIUM_CALENDAR_CALENDAR_DIR not set: iCloud mirror skipped")
		}
	}
	if filter.Target == "" || filter.Target == Google {
		found, err := googleEvents(ctx, env, set, ws, we, tz)
		var missing NotConfigured
		switch {
		case errors.As(err, &missing):
			notes = append(notes, "google: "+missing.Error())
		case err != nil:
			notes = append(notes, "google: "+err.Error())
			googleErr = err
		default:
			events = append(events, found...)
			reached++
		}
	}
	if reached == 0 {
		if googleErr != nil {
			return nil, googleErr
		}
		text := strings.Join(notes, "; ")
		if text == "" {
			text = "no calendar source configured"
		}
		return nil, NotConfigured(text)
	}
	sortEvents(events, tz)
	truncated := len(events) > set.MaxEvents
	if truncated {
		events = events[:set.MaxEvents]
	}
	return &Listing{Events: events, Truncated: truncated, Notes: notes, tz: tz}, nil
}

func sortEvents(events []Row, tz *time.Location) {
	keys := make([]time.Time, len(events))
	for i, ev := range events {
		keys[i] = asDT(ev.Start, tz)
	}
	order := make([]int, len(events))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ka, kb := keys[order[a]], keys[order[b]]
		if !ka.Equal(kb) {
			return ka.Before(kb)
		}
		return events[order[a]].Title < events[order[b]].Title
	})
	sorted := make([]Row, len(events))
	for i, at := range order {
		sorted[i] = events[at]
	}
	copy(events, sorted)
}
