// Package calendar is calendar.py: the vdirsyncer iCloud mirror (read), Google Calendar (read and write) and iCloud
// CalDAV (write), and the agent_events log of what atrium created. Events never carry attendees: specs hold
// title, times, location and notes, and nothing here emits ATTENDEE, ORGANIZER or invitation mail (Google inserts
// use sendUpdates="none").
package calendar

import (
	"context"
	"database/sql"
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
	ICloud         = "icloud"
	Google         = "google"
	CalDAVURL      = "https://caldav.icloud.com/"
	TokenURI       = "https://oauth2.googleapis.com/token"
	Scope          = "https://www.googleapis.com/auth/calendar.events"
	MaxEvents      = 200
	MaxOccurrences = 2000
	DefaultMinutes = 60
	remoteTimeout  = 60 * time.Second
)

// Targets is TARGETS.
var Targets = [...]string{ICloud, Google}

// TargetsText is how errors list the targets.
var TargetsText = strings.Join(Targets[:], ", ")

// Endpoints are the services calendar talks to; tests point them at recorded exchanges.
type Endpoints struct {
	Token     string
	GoogleAPI string
	CalDAV    string
}

var endpoints = Endpoints{Token: TokenURI, CalDAV: CalDAVURL}

// UseEndpoints replaces the service endpoints (an empty field keeps the real one) and returns the restore func.
func UseEndpoints(e Endpoints) func() {
	saved := endpoints
	if e.Token != "" {
		endpoints.Token = e.Token
	}
	if e.GoogleAPI != "" {
		endpoints.GoogleAPI = e.GoogleAPI
	}
	if e.CalDAV != "" {
		endpoints.CalDAV = e.CalDAV
	}
	return func() { endpoints = saved }
}

// Ref is the remote reference create_event returns.
type Ref struct {
	Calendar string
	UID      string
}

// Dict is the reference as Python returns it.
func (r Ref) Dict() py.Dict {
	return py.Dict{{Key: "calendar", Value: r.Calendar}, {Key: "uid", Value: r.UID}}
}

// Zone is tz_of: the location of ATRIUM_TZ.
func Zone(env config.Env) (*time.Location, error) { return config.Location(env) }

func targetError() error { return Error("calendar must be one of " + TargetsText) }

type acceptJSON struct{ base http.RoundTripper }

func (a acceptJSON) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Accept") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("Accept", "application/json")
	}
	return a.base.RoundTrip(r)
}

func googleService(ctx context.Context, env config.Env) (*gcal.Service, error) {
	creds, err := config.Google(env)
	if err != nil {
		return nil, NotConfigured(err.Error())
	}
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

// CreateEvent is create_event: spec on calendar, returning the remote reference.
func CreateEvent(ctx context.Context, env config.Env, target string, spec Spec, tz *time.Location) (Ref, error) {
	switch target {
	case Google:
		svc, err := googleService(ctx, env)
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
		cal, err := openICloud(ctx, env)
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
	return Ref{Calendar: target, UID: spec.UID}, nil
}

// DeleteEvent is delete_event: the event with this uid is removed from calendar; one already gone is not an error.
func DeleteEvent(ctx context.Context, env config.Env, target, uid string) error {
	switch target {
	case Google:
		svc, err := googleService(ctx, env)
		if err != nil {
			return err
		}
		if err := svc.Events.Delete("primary", uid).SendUpdates("none").Context(ctx).Do(); err != nil {
			if status := httpStatus(err); status != 404 && status != 410 {
				return remote(err)
			}
		}
	case ICloud:
		cal, err := openICloud(ctx, env)
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

// Track is track: the log row of an event atrium created.
func Track(ctx context.Context, q db.Querier, spec Spec, target string, proposalID *int64, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT OR REPLACE INTO agent_events (uid, calendar, title, start, end, spec, created_ts, deleted_ts, proposal_id) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)",
		spec.UID, target, spec.Title, spec.Start, spec.End, py.JSONDumps(spec.Dict()), now, proposalID)
	return err
}

// Tracked is a live agent_events row.
type Tracked struct {
	Calendar string
	Spec     string
}

// FindTracked is tracked: the live row for uid, nil when atrium did not create it or it was deleted.
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

// Untrack is untrack.
func Untrack(ctx context.Context, q db.Querier, uid string, now float64) error {
	_, err := q.ExecContext(ctx, "UPDATE agent_events SET deleted_ts = ? WHERE uid = ?", now, uid)
	return err
}

// Listing is what list_events returns.
type Listing struct {
	Events    []Row
	Truncated bool
	Notes     []string
}

// Dict is the listing as the tool returns it.
func (l Listing) Dict() py.Dict {
	events := make([]any, len(l.Events))
	for i, row := range l.Events {
		events[i] = row.Dict()
	}
	notes := make([]any, len(l.Notes))
	for i, note := range l.Notes {
		notes[i] = note
	}
	return py.Dict{{Key: "events", Value: events}, {Key: "truncated", Value: l.Truncated}, {Key: "notes", Value: notes}}
}

// ListEvents is list_events: the events between start and end (end exclusive) from the mirror and the primary
// Google calendar, merged and sorted. start, end and target are the tool's arguments, nil or "" when absent.
func ListEvents(ctx context.Context, env config.Env, start, end any, target string) (*Listing, error) {
	tz, err := Zone(env)
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
	wanted := strings.ToLower(py.Strip(target))
	events, notes := []Row{}, []string{}
	reached := 0
	if wanted != Google {
		if dir := config.CalendarDir(env); dir != "" {
			found, extra, err := MirrorEvents(dir, ws, we, tz, wanted)
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
			notes = append(notes, "ATRIUM_CALENDAR_DIR not set: iCloud mirror skipped")
		}
	}
	if wanted == "" || wanted == Google {
		found, err := googleEvents(ctx, env, ws, we, tz)
		var missing NotConfigured
		switch {
		case errors.As(err, &missing):
			notes = append(notes, "google: "+missing.Error())
		case err != nil:
			notes = append(notes, "google: "+py.ExceptionText(err))
		default:
			events = append(events, found...)
			reached++
		}
	}
	if reached == 0 {
		text := strings.Join(notes, "; ")
		if text == "" {
			text = "no calendar source configured"
		}
		return nil, NotConfigured(text)
	}
	if err := sortEvents(events, tz); err != nil {
		return nil, err
	}
	truncated := len(events) > MaxEvents
	if truncated {
		events = events[:MaxEvents]
	}
	return &Listing{Events: events, Truncated: truncated, Notes: notes}, nil
}

func sortEvents(events []Row, tz *time.Location) error {
	keys := make([]time.Time, len(events))
	for i, ev := range events {
		v, err := ParseWhen(ev.Start, tz, !strings.Contains(ev.Start, "T"))
		if err != nil {
			return err
		}
		keys[i] = asDT(v, tz)
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
	return nil
}
