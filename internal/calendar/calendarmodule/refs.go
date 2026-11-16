package calendarmodule

import (
	"context"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func eventRef(env config.Env) module.RefKind {
	return module.RefKind{Kind: "event", Resolve: func(ctx context.Context, q db.Querier, raw string) (*module.Ref, error) {
		day, uid, _ := strings.Cut(raw, ":")
		start, err := time.Parse(time.DateOnly, day)
		if err != nil || calendar.EventRef(day, uid) == "" || !module.IsEnabled(ctx, unit.Calendar) {
			return nil, nil
		}
		set, err := calendar.LoadSettings(ctx, q)
		if err != nil {
			return nil, err
		}
		listing, err := calendar.ListEvents(ctx, env, set, day, start.AddDate(0, 0, 1).Format(time.DateOnly), calendar.Filter{})
		if err != nil {
			return nil, nil
		}
		for _, event := range listing.Events {
			if event.UID == uid {
				return &module.Ref{Ref: calendar.EventRef(day, uid), Label: event.Title, Line: "About the calendar event with uid " + uid + " on " + day + "."}, nil
			}
		}
		return nil, nil
	}}
}
