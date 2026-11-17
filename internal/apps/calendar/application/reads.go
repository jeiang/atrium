package application

import (
	"context"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/calendar"
)

type ListInput struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Calendar string `json:"calendar,omitempty"`
	Detail   bool   `json:"detail,omitempty"`
}

type CalendarsInput struct{}

type CalendarList struct {
	Calendars []calendar.Source `json:"calendars"`
	Today     string            `json:"today"`
	Timezone  string            `json:"timezone"`
}

type Listing struct {
	Value     map[string]any `json:"value"`
	Messages  bool           `json:"-"`
	Untrusted bool           `json:"-"`
	PII       bool           `json:"-"`
}

type FindSlotInput struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Minutes  int64  `json:"duration_minutes"`
	Calendar string `json:"calendar,omitempty"`
}

type Slots struct {
	Value    map[string]any `json:"value"`
	Messages bool           `json:"-"`
}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[Listing], error) {
	filter, err := calendar.ParseFilter(in.Calendar)
	if err != nil {
		return application.Read[Listing]{}, classify(err)
	}
	set, err := calendar.LoadSettings(ctx, s.db.Read)
	if err != nil {
		return application.Read[Listing]{}, err
	}
	listing, err := calendar.ListEvents(ctx, s.env, set, in.Start, in.End, filter)
	if err != nil {
		return application.Read[Listing]{}, classify(err)
	}
	marked := len(listing.Events) > 0
	value := listing.Dict()
	if in.Detail {
		value = listing.Detail(time.UnixMicro(int64(s.now() * 1e6)))
	}
	out := Listing{Value: value, Messages: marked, Untrusted: marked, PII: slices.ContainsFunc(listing.Events, func(r calendar.Row) bool { return r.Calendar == calendar.Birthdays })}
	rev, err := s.read(ctx, ListName, []string{state.Calendar, state.Settings, state.Connections}, in, out.Value)
	if err != nil {
		return application.Read[Listing]{}, err
	}
	return application.Read[Listing]{Revision: rev, Data: out}, nil
}

func (s *Service) Calendars(ctx context.Context, _ CalendarsInput) (application.Read[CalendarList], error) {
	set, err := calendar.LoadSettings(ctx, s.db.Read)
	if err != nil {
		return application.Read[CalendarList]{}, err
	}
	tz, err := calendar.Zone(ctx, s.env)
	if err != nil {
		return application.Read[CalendarList]{}, err
	}
	out := CalendarList{Calendars: calendar.Sources(ctx, set), Today: time.UnixMicro(int64(s.now() * 1e6)).In(tz).Format(time.DateOnly), Timezone: tz.String()}
	rev, err := s.read(ctx, CalendarsName, []string{state.Calendar, state.Settings, state.Connections}, nil, out)
	if err != nil {
		return application.Read[CalendarList]{}, err
	}
	return application.Read[CalendarList]{Revision: rev, Data: out}, nil
}

func (s *Service) FindSlot(ctx context.Context, in FindSlotInput) (application.Read[Slots], error) {
	filter, err := calendar.ParseFilter(in.Calendar)
	if err != nil {
		return application.Read[Slots]{}, classify(err)
	}
	set, err := calendar.LoadSettings(ctx, s.db.Read)
	if err != nil {
		return application.Read[Slots]{}, err
	}
	found, err := calendar.FindSlots(ctx, s.db.Read, s.env, set, in.Start, in.End, time.Duration(in.Minutes)*time.Minute, filter)
	if err != nil {
		return application.Read[Slots]{}, classify(err)
	}
	windows := make([]any, len(found.Windows))
	for i, w := range found.Windows {
		windows[i] = map[string]any{"start": w.Start.Format(time.RFC3339), "end": w.End.Format(time.RFC3339)}
	}
	notes := make([]any, len(found.Notes))
	for i, n := range found.Notes {
		notes[i] = n
	}
	out := Slots{Value: map[string]any{"windows": windows, "notes": notes}, Messages: true}
	rev, err := s.read(ctx, FindSlotName, []string{state.Calendar, state.Settings, state.Connections}, in, out.Value)
	if err != nil {
		return application.Read[Slots]{}, err
	}
	return application.Read[Slots]{Revision: rev, Data: out}, nil
}
