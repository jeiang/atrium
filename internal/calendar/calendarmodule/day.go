package calendarmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/day"
	"github.com/jeiang/atrium/internal/db"
)

const eventKind = "event"

type dayProvider struct {
	env config.Env
}

func (dayProvider) Kinds() []string { return []string{eventKind} }

func (p dayProvider) Entries(ctx context.Context, q db.Querier, from, to time.Time) ([]day.Entry, error) {
	tz, err := calendar.Zone(ctx, p.env)
	if err != nil {
		return nil, err
	}
	set, err := calendar.LoadSettings(ctx, q)
	if err != nil {
		return nil, err
	}
	listing, err := calendar.ListEvents(ctx, p.env, set, from.Format(time.RFC3339), to.Format(time.RFC3339), calendar.Filter{})
	if err != nil {
		return nil, &application.Error{Code: application.Unavailable, Message: "The calendar is unavailable.", Cause: err}
	}
	out := make([]day.Entry, 0, len(listing.Events))
	for _, row := range listing.Events {
		fields := row.Dict(tz)
		first, _ := fields["start"].(string)
		last, _ := fields["end"].(string)
		entry := day.Entry{Title: row.Title, Location: row.Location, Kind: eventKind}
		start, err := time.ParseInLocation("2006-01-02T15:04:05", first[:min(len(first), 19)], tz)
		if err == nil {
			entry.Start = start
			if end, err := time.ParseInLocation("2006-01-02T15:04:05", last[:min(len(last), 19)], tz); err == nil {
				entry.End = end
			}
		} else {
			entry.AllDay = true
			entry.Start, _ = time.ParseInLocation(time.DateOnly, first[:min(len(first), 10)], tz)
		}
		refDay := from.In(tz).Format(time.DateOnly)
		if entry.Start.After(from) {
			refDay = entry.Start.Format(time.DateOnly)
		}
		entry.Ref = calendar.EventRef(refDay, row.UID)
		out = append(out, entry)
	}
	return out, nil
}
