package application

import (
	"context"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/calendar"
)

const (
	EventName       application.Name = "calendar.event"
	CreateEventName application.Name = "calendar.event-create"
	UpdateEventName application.Name = "calendar.event-update"
	DeleteEventName application.Name = "calendar.event-delete"
)

type EventRef struct {
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
}

type EventCreateInput struct {
	Calendar string  `json:"calendar"`
	Title    string  `json:"title"`
	Start    string  `json:"start"`
	End      *string `json:"end,omitempty"`
	AllDay   bool    `json:"all_day,omitempty"`
	Location *string `json:"location,omitempty"`
	Notes    *string `json:"notes,omitempty"`
}

type EventUpdateInput struct {
	Calendar string  `json:"calendar"`
	UID      string  `json:"uid"`
	IfMatch  string  `json:"if_match"`
	Title    *string `json:"title,omitempty"`
	Start    *string `json:"start,omitempty"`
	End      *string `json:"end,omitempty"`
	AllDay   *bool   `json:"all_day,omitempty"`
	Location *string `json:"location,omitempty"`
	Notes    *string `json:"notes,omitempty"`
}

type EventDeleteInput struct {
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
	IfMatch  string `json:"if_match,omitempty"`
}

func DescribeWrites() []application.Definition {
	operator := []application.Authority{application.Operator}
	return []application.Definition{
		application.Describe[EventRef, calendar.Event](EventName, application.Query, operator, application.NoEffect, false),
		application.Describe[EventCreateInput, calendar.Event](CreateEventName, application.Command, operator, application.External, true),
		application.Describe[EventUpdateInput, calendar.Event](UpdateEventName, application.Command, operator, application.External, true),
		application.Describe[EventDeleteInput, Deleted](DeleteEventName, application.Command, operator, application.External, true),
	}
}

func (s *Service) writer() *calendar.Proposals {
	return &calendar.Proposals{DB: s.db, Ledger: s.ledger, Now: s.now, Env: s.env}
}

func (s *Service) Event(ctx context.Context, in EventRef) (application.Read[calendar.Event], error) {
	target, err := calendar.ParseTarget(in.Calendar)
	if err != nil {
		return application.Read[calendar.Event]{}, classifyWrite(err, 0)
	}
	tz, err := calendar.Zone(ctx, s.env)
	if err != nil {
		return application.Read[calendar.Event]{}, err
	}
	set, err := calendar.LoadSettings(ctx, s.db.Read)
	if err != nil {
		return application.Read[calendar.Event]{}, err
	}
	event, err := calendar.ReadEvent(ctx, s.env, set, target, in.UID, tz)
	if err != nil {
		return application.Read[calendar.Event]{}, classifyWrite(err, 0)
	}
	return application.Read[calendar.Event]{Revision: application.Revision(event.Revision), Data: event}, nil
}

func (s *Service) CreateEvent(ctx context.Context, in EventCreateInput) (application.Change[calendar.Event], error) {
	details := map[string]any{"calendar": in.Calendar}
	target, err := calendar.ParseTarget(in.Calendar)
	if err != nil {
		return application.Change[calendar.Event]{}, s.fail(ctx, CreateEventName, in.Calendar, details, classifyWrite(err, 0))
	}
	raw := map[string]any{"title": in.Title, "start": in.Start, "all_day": in.AllDay}
	for key, value := range map[string]*string{"end": in.End, "location": in.Location, "notes": in.Notes} {
		if value != nil {
			raw[key] = *value
		}
	}
	event, id, err := s.writer().CreateDirect(ctx, target, raw)
	return s.written(ctx, CreateEventName, string(target), details, event, id, err)
}

func (s *Service) UpdateEvent(ctx context.Context, in EventUpdateInput) (application.Change[calendar.Event], error) {
	details := map[string]any{"calendar": in.Calendar, "uid": in.UID}
	target, err := calendar.ParseTarget(in.Calendar)
	if err != nil {
		return application.Change[calendar.Event]{}, s.fail(ctx, UpdateEventName, in.Calendar, details, classifyWrite(err, 0))
	}
	if strings.TrimSpace(in.IfMatch) == "" {
		return application.Change[calendar.Event]{}, s.fail(ctx, UpdateEventName, string(target), details, &application.Error{
			Code: application.Invalid, Message: "Send the event's revision in If-Match; read the event first.",
			Fields: []application.Field{{Name: "If-Match", Code: "missing", Message: "Field required"}}})
	}
	changes := map[string]any{}
	for key, value := range map[string]*string{"title": in.Title, "start": in.Start, "end": in.End, "location": in.Location, "notes": in.Notes} {
		if value != nil {
			changes[key] = *value
		}
	}
	if in.AllDay != nil {
		changes["all_day"] = *in.AllDay
	}
	if len(changes) == 0 {
		return application.Change[calendar.Event]{}, s.fail(ctx, UpdateEventName, string(target), details, &application.Error{
			Code: application.Invalid, Message: "Give a title, start, end, all_day, location or notes to change."})
	}
	event, id, err := s.writer().UpdateDirect(ctx, target, in.UID, in.IfMatch, changes)
	return s.written(ctx, UpdateEventName, string(target), details, event, id, err)
}

func (s *Service) DeleteEvent(ctx context.Context, in EventDeleteInput) (application.Change[Deleted], error) {
	details := map[string]any{"calendar": in.Calendar, "uid": in.UID}
	target, err := calendar.ParseTarget(in.Calendar)
	if err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, DeleteEventName, in.Calendar, details, classifyWrite(err, 0))
	}
	id, err := s.writer().DeleteDirect(ctx, target, in.UID, in.IfMatch)
	if id != 0 {
		details["effect_ids"] = []int64{id}
	}
	if err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, DeleteEventName, string(target), details, classifyWrite(err, id))
	}
	audit := s.settled(ctx, DeleteEventName, state.OK, string(target), details)
	return application.Change[Deleted]{Data: Deleted{Deleted: in.UID, Calendar: string(target)}, EffectIDs: []int64{id}, AuditID: audit}, nil
}

func (s *Service) written(ctx context.Context, name application.Name, target string, details map[string]any, event calendar.Event, id int64, err error) (application.Change[calendar.Event], error) {
	if id != 0 {
		details["effect_ids"] = []int64{id}
	}
	if err != nil {
		return application.Change[calendar.Event]{}, s.fail(ctx, name, target, details, classifyWrite(err, id))
	}
	details["uid"] = event.UID
	audit := s.settled(ctx, name, state.OK, target, details)
	return application.Change[calendar.Event]{Data: event, EffectIDs: []int64{id}, AuditID: audit}, nil
}

func classifyWrite(err error, effect int64) error {
	var missing calendar.Missing
	var stale calendar.Stale
	var blocked calendar.Blocked
	var out *application.Error
	switch {
	case errors.As(err, &blocked):
		return &application.Error{Code: application.Refused, Message: blocked.Error(), EffectIDs: []int64{blocked.ID}, Cause: err}
	case errors.As(err, &stale):
		out = &application.Error{Code: application.Precondition, Message: stale.Error(), Cause: err}
	case errors.As(err, &missing):
		out = &application.Error{Code: application.NotFound, Message: missing.Error(), Cause: err}
	case calendar.OutcomeUnknown(err):
		out = &application.Error{Code: application.Unknown, Message: err.Error() + "; check the calendar, then resolve the effect on the Activity page", Cause: err}
	default:
		if !errors.As(classify(err), &out) {
			return err
		}
	}
	if effect != 0 {
		copied := *out
		copied.EffectIDs = []int64{effect}
		return &copied
	}
	return out
}
