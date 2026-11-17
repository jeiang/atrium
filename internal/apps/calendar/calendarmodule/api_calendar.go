package calendarmodule

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/calendar"
	calapp "github.com/jeiang/atrium/internal/apps/calendar/application"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type EventsRequest struct {
	From     string `query:"from" json:"-"`
	To       string `query:"to" json:"-"`
	Calendar string `query:"calendar" json:"-"`
}

type Event struct {
	UID       string `json:"uid"`
	Calendar  string `json:"calendar"`
	Target    string `json:"target,omitempty" api:"enum=icloud|google|birthdays"`
	ReadOnly  bool   `json:"read_only,omitempty"`
	Start     string `json:"start"`
	End       string `json:"end"`
	AllDay    bool   `json:"all_day,omitempty"`
	Title     string `json:"title"`
	Location  string `json:"location"`
	Notes     string `json:"notes,omitempty"`
	Recurring bool   `json:"recurring,omitempty"`
	Revision  string `json:"revision,omitempty"`
}

type Events struct {
	Events    []Event  `json:"events"`
	Truncated bool     `json:"truncated"`
	Notes     []string `json:"notes"`
	Today     string   `json:"today,omitempty"`
	Timezone  string   `json:"timezone,omitempty"`
}

type FreeSlotsRequest struct {
	From     string `query:"from" json:"-"`
	To       string `query:"to" json:"-"`
	Minutes  int64  `query:"duration_minutes" json:"-" api:"min=1"`
	Calendar string `query:"calendar" json:"-"`
}

type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type FreeSlots struct {
	Windows []Window `json:"windows"`
	Notes   []string `json:"notes"`
}

func retype[T any](v map[string]any) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func (m Module) calendarRoutes(d *web.Deps) []application.Binding {
	reads := func() *calapp.Service { return calapp.New(d.DB, d.Env, nil, calendar.Now) }
	return append([]application.Binding{
		api.Query(d, calapp.Describe()[0], application.APIShape{Area: "home", Errors: []application.Code{application.Invalid}},
			"GET /calendar/events", func(ctx context.Context, in EventsRequest) (application.Read[Events], error) {
				read, err := reads().List(ctx, calapp.ListInput{Start: in.From, End: in.To, Calendar: in.Calendar, Detail: true})
				if err != nil {
					return application.Read[Events]{}, err
				}
				out, err := retype[Events](read.Data.Value)
				return application.Read[Events]{Revision: read.Revision, Data: out}, err
			}),
		api.Query(d, calapp.DescribeCalendars(), application.APIShape{Area: "home", Description: "The calendars events can be read from and written to. The id is the calendar path segment of the event routes and the calendar of POST /calendar/events; read_only calendars take no writes. today and timezone are the server's date and zone."},
			"GET /calendar/calendars", func(ctx context.Context, in calapp.CalendarsInput) (application.Read[calapp.CalendarList], error) {
				return reads().Calendars(ctx, in)
			}),
		api.Query(d, calapp.Describe()[1], application.APIShape{Area: "home", Errors: []application.Code{application.Invalid}},
			"GET /calendar/free-slots", func(ctx context.Context, in FreeSlotsRequest) (application.Read[FreeSlots], error) {
				read, err := reads().FindSlot(ctx, calapp.FindSlotInput{Start: in.From, End: in.To, Minutes: in.Minutes, Calendar: in.Calendar})
				if err != nil {
					return application.Read[FreeSlots]{}, err
				}
				out, err := retype[FreeSlots](read.Data.Value)
				return application.Read[FreeSlots]{Revision: read.Revision, Data: out}, err
			}),
		api.Command(d, calapp.DescribePasses()[0], application.APIShape{Area: "integrations", Errors: []application.Code{application.NotConfigured, application.Unavailable, application.Conflict}},
			"POST /calendar/sync", func(ctx context.Context, in calapp.SyncInput) (application.Change[davmirror.Stats], error) {
				change, err := reads().SyncManual(ctx, d.Cfg)
				if unmet, ok := errors.AsType[*connections.Unmet](err); ok {
					return change, &application.Error{Code: application.Unavailable, Message: unmet.Error(), Cause: err}
				}
				return change, syncBusy(err, "A calendar sync is already running.")
			}),
	}, eventRoutes(d)...)
}

func syncBusy(err error, message string) error {
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Conflict, Message: message, Cause: err}
	}
	return err
}

type EventPath struct {
	Calendar string `path:"calendar" json:"-"`
	UID      string `path:"uid" json:"-"`
}

type EventCreate struct {
	Calendar string  `json:"calendar" api:"required"`
	Title    string  `json:"title" api:"required"`
	Start    string  `json:"start" api:"required"`
	End      *string `json:"end,omitempty"`
	AllDay   bool    `json:"all_day,omitempty"`
	Location *string `json:"location,omitempty"`
	Notes    *string `json:"notes,omitempty"`
}

type EventPatch struct {
	Calendar string  `path:"calendar" json:"-"`
	UID      string  `path:"uid" json:"-"`
	IfMatch  string  `header:"If-Match" json:"-"`
	Title    *string `json:"title,omitempty"`
	Start    *string `json:"start,omitempty"`
	End      *string `json:"end,omitempty"`
	AllDay   *bool   `json:"all_day,omitempty"`
	Location *string `json:"location,omitempty"`
	Notes    *string `json:"notes,omitempty"`
}

type EventDelete struct {
	Calendar string `path:"calendar" json:"-"`
	UID      string `path:"uid" json:"-"`
	IfMatch  string `header:"If-Match" json:"-"`
}

func eventRoutes(d *web.Deps) []application.Binding {
	var writes *calapp.Service
	if d != nil {
		effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, d.Cfg, calendar.Now)
		if err != nil {
			panic(err)
		}
		d.DrainOnStop(effects.Drain)
		writes = calapp.New(d.DB, d.Env, effects, calendar.Now)
	}
	defs := calapp.DescribeWrites()
	shape := func(description string, codes ...application.Code) application.APIShape {
		base := []application.Code{application.Invalid, application.Refused, application.NotConfigured, application.Unavailable}
		return application.APIShape{Area: "home", Description: description, Errors: append(base, codes...)}
	}
	get := shape("Reads one event live from its provider. The ETag and the revision field carry the event's revision for If-Match.", application.NotFound)
	create := shape("Creates an event in icloud or google. Each write records an undoable effect; undo it with POST /effects/{id}/undo. Birthdays is read-only.", application.Unknown)
	create.Success = 201
	patch := shape("Changes any of title, start, end, all_day, location and notes. If-Match is required: send the revision from the event read; a stale revision gets 412 and nothing is written.", application.NotFound, application.Precondition, application.Unknown)
	remove := shape("Deletes the event. If-Match is optional: with it, a stale revision gets 412 and nothing is deleted. Undo puts the event back.", application.NotFound, application.Precondition, application.Unknown)
	remove.Body = application.BodyNone
	return []application.Binding{
		api.Query(d, defs[0], get, "GET /calendar/events/{calendar}/{uid}", func(ctx context.Context, in EventPath) (application.Read[calendar.Event], error) {
			return writes.Event(ctx, calapp.EventRef{Calendar: in.Calendar, UID: in.UID})
		}),
		api.Command(d, defs[1], create, "POST /calendar/events", func(ctx context.Context, in EventCreate) (application.Change[calendar.Event], error) {
			return writes.CreateEvent(ctx, calapp.EventCreateInput{Calendar: in.Calendar, Title: in.Title, Start: in.Start, End: in.End, AllDay: in.AllDay, Location: in.Location, Notes: in.Notes})
		}),
		api.Command(d, defs[2], patch, "PATCH /calendar/events/{calendar}/{uid}", func(ctx context.Context, in EventPatch) (application.Change[calendar.Event], error) {
			return writes.UpdateEvent(ctx, calapp.EventUpdateInput{Calendar: in.Calendar, UID: in.UID, IfMatch: in.IfMatch,
				Title: in.Title, Start: in.Start, End: in.End, AllDay: in.AllDay, Location: in.Location, Notes: in.Notes})
		}),
		api.Command(d, defs[3], remove, "DELETE /calendar/events/{calendar}/{uid}", func(ctx context.Context, in EventDelete) (application.Change[calapp.Deleted], error) {
			return writes.DeleteEvent(ctx, calapp.EventDeleteInput{Calendar: in.Calendar, UID: in.UID, IfMatch: in.IfMatch})
		}),
	}
}
