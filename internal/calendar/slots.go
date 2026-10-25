package calendar

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	WorkHoursKey = "work_hours"
	WorkDaysKey  = "work_days"
)

var workWeekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func init() {
	instance := []settings.Scope{settings.Instance}
	settings.MustRegister(
		settings.Def{Key: WorkHoursKey, Module: "calendar", Type: settings.String, Default: "09:00-17:00",
			Check: func(v string) error { _, _, err := parseHours(v); return err }, Class: settings.UI, Scopes: instance},
		settings.Def{Key: WorkDaysKey, Module: "calendar", Type: settings.String, Default: "mon,tue,wed,thu,fri",
			Check: func(v string) error { _, err := parseDays(v); return err }, Class: settings.UI, Scopes: instance},
	)
}

func clock(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

func parseHours(v string) (from, to time.Duration, err error) {
	a, b, ok := strings.Cut(v, "-")
	bad := settings.Invalid("must be HH:MM-HH:MM, such as 09:00-17:00")
	if !ok {
		return 0, 0, bad
	}
	if from, err = clock(a); err != nil {
		return 0, 0, bad
	}
	if to, err = clock(b); err != nil || to <= from {
		return 0, 0, bad
	}
	return from, to, nil
}

func parseDays(v string) (map[time.Weekday]bool, error) {
	out := map[time.Weekday]bool{}
	for _, part := range strings.Split(v, ",") {
		d, ok := workWeekdays[strings.ToLower(strings.TrimSpace(part))]
		if !ok {
			return nil, settings.Invalid("must be day names such as mon,tue,wed,thu,fri")
		}
		out[d] = true
	}
	return out, nil
}

type Window struct {
	Start, End time.Time
}

func FreeWindows(busy []Window, from, to time.Time, need time.Duration, dayStart, dayEnd time.Duration, days map[time.Weekday]bool, tz *time.Location) []Window {
	slices.SortFunc(busy, func(a, b Window) int { return a.Start.Compare(b.Start) })
	var out []Window
	first := from.In(tz)
	for day := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, tz); day.Before(to); day = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, tz) {
		if !days[day.Weekday()] {
			continue
		}
		open := time.Date(day.Year(), day.Month(), day.Day(), int(dayStart.Hours()), int(dayStart.Minutes())%60, 0, 0, tz)
		closeAt := time.Date(day.Year(), day.Month(), day.Day(), int(dayEnd.Hours()), int(dayEnd.Minutes())%60, 0, 0, tz)
		if open.Before(from) {
			open = from
		}
		if closeAt.After(to) {
			closeAt = to
		}
		cursor := open
		for _, b := range busy {
			if !b.End.After(cursor) || !b.Start.Before(closeAt) {
				continue
			}
			if b.Start.Sub(cursor) >= need {
				out = append(out, Window{cursor, b.Start})
			}
			if b.End.After(cursor) {
				cursor = b.End
			}
		}
		if closeAt.Sub(cursor) >= need {
			out = append(out, Window{cursor, closeAt})
		}
	}
	return out
}

type Slots struct {
	Windows []Window
	Notes   []string
}

func FindSlots(ctx context.Context, q db.Querier, env config.Env, set Settings, start, end any, need time.Duration, target string) (Slots, error) {
	tz, err := Zone(env)
	if err != nil {
		return Slots{}, err
	}
	hours, err := settings.Default.Resolve(ctx, q, WorkHoursKey, 0)
	if err != nil {
		return Slots{}, err
	}
	dayStart, dayEnd, err := parseHours(hours.Value)
	if err != nil {
		return Slots{}, err
	}
	daysSetting, err := settings.Default.Resolve(ctx, q, WorkDaysKey, 0)
	if err != nil {
		return Slots{}, err
	}
	days, err := parseDays(daysSetting.Value)
	if err != nil {
		return Slots{}, err
	}
	first, err := ParseWhen(start, tz, false)
	if err != nil {
		return Slots{}, err
	}
	last, err := ParseWhen(end, tz, false)
	if err != nil {
		return Slots{}, err
	}
	listing, err := ListEvents(ctx, env, set, start, end, target)
	if err != nil {
		return Slots{}, err
	}
	var busy []Window
	for _, row := range listing.Events {
		s, err := ParseWhen(row.Start, tz, false)
		if err != nil {
			return Slots{}, err
		}
		if !strings.Contains(row.Start, "T") {
			continue
		}
		e, err := ParseWhen(row.End, tz, false)
		if err != nil {
			return Slots{}, err
		}
		busy = append(busy, Window{asDT(s, tz), asDT(e, tz)})
	}
	notes := listing.Notes
	if listing.Truncated {
		notes = append(notes, fmt.Sprintf("more than %d events in the range; later busy time may be missing", set.MaxEvents))
	}
	return Slots{Windows: FreeWindows(busy, asDT(first, tz), asDT(last, tz), need, dayStart, dayEnd, days, tz), Notes: notes}, nil
}
