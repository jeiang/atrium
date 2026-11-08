package calendar

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jeiang/atrium/internal/httpx"
	gcal "google.golang.org/api/calendar/v3"
)

type Spec struct {
	UID      string
	Title    string
	Start    Value
	End      Value
	AllDay   bool
	Location string
	Notes    string
}

func (s Spec) Dict() map[string]any {
	return map[string]any{
		"uid": s.UID, "title": s.Title, "start": s.Start.iso(), "end": s.End.iso(),
		"all_day": s.AllDay, "location": s.Location, "notes": s.Notes,
	}
}

func SpecOf(v any) (Spec, error) {
	d, ok := v.(map[string]any)
	if !ok {
		return Spec{}, fmt.Errorf("%w: spec is %T, not an object", httpx.ErrShape, v)
	}
	var s Spec
	text := func(key string) (string, error) {
		val, found := d[key]
		if !found {
			return "", fmt.Errorf("%w: %q", httpx.ErrMissing, key)
		}
		str, isString := val.(string)
		if !isString {
			return "", fmt.Errorf("%w: spec %s is %T, not a string", httpx.ErrShape, key, val)
		}
		return str, nil
	}
	var err error
	for _, field := range []struct {
		key string
		dst *string
	}{{"uid", &s.UID}, {"title", &s.Title}, {"location", &s.Location}, {"notes", &s.Notes}} {
		if *field.dst, err = text(field.key); err != nil {
			return Spec{}, err
		}
	}
	allDay, found := d["all_day"]
	if !found {
		return Spec{}, fmt.Errorf("%w: 'all_day'", httpx.ErrMissing)
	}
	s.AllDay, _ = allDay.(bool)
	for _, field := range []struct {
		key string
		dst *Value
	}{{"start", &s.Start}, {"end", &s.End}} {
		raw, err := text(field.key)
		if err != nil {
			return Spec{}, err
		}
		if *field.dst, err = ParseWhen(raw, time.UTC, s.AllDay); err != nil {
			return Spec{}, err
		}
	}
	return s, nil
}

func NewUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}

func textField(raw map[string]any, key string) (string, error) {
	v := raw[key]
	if v == nil || v == "" {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s is %T, not a string", httpx.ErrShape, key, v)
	}
	return strings.TrimSpace(s), nil
}

func NormalizeSpec(raw map[string]any, tz *time.Location, uid string, defaultMinutes int) (Spec, error) {
	title, err := textField(raw, "title")
	if err != nil {
		return Spec{}, err
	}
	if title == "" {
		return Spec{}, Error("title is required")
	}
	allDay, _ := raw["all_day"].(bool)
	startRaw := raw["start"]
	start, err := ParseWhen(startRaw, tz, allDay)
	if err != nil {
		return Spec{}, err
	}
	var end Value
	given, _ := raw["end"].(string)
	switch {
	case given != "":
		if end, err = ParseWhen(given, tz, allDay); err != nil {
			return Spec{}, err
		}
	case allDay:
		end = dateValue(start.t.Year(), start.t.Month(), start.t.Day()+1)
	default:
		end = awareValue(after(start, time.Duration(defaultMinutes)*time.Minute, tz, 0).t.In(start.t.Location()))
	}
	if !end.t.After(start.t) {
		return Spec{}, Error("end must be after start")
	}
	if uid == "" {
		if s, ok := raw["uid"].(string); ok {
			uid = s
		}
	}
	if uid == "" {
		uid = NewUID()
	}
	location, err := textField(raw, "location")
	if err != nil {
		return Spec{}, err
	}
	notes, err := textField(raw, "notes")
	if err != nil {
		return Spec{}, err
	}
	return Spec{UID: uid, Title: title, Start: start, End: end, AllDay: allDay, Location: location, Notes: notes}, nil
}

func GoogleBody(s Spec, tz *time.Location) (*gcal.Event, error) {
	body := &gcal.Event{Id: s.UID, Summary: s.Title}
	if s.AllDay {
		body.Start = &gcal.EventDateTime{Date: s.Start.iso()}
		body.End = &gcal.EventDateTime{Date: s.End.iso()}
	} else {
		zone := tz.String()
		body.Start = &gcal.EventDateTime{DateTime: s.Start.iso(), TimeZone: zone}
		body.End = &gcal.EventDateTime{DateTime: s.End.iso(), TimeZone: zone}
	}
	body.Location = s.Location
	body.Description = s.Notes
	return body, nil
}

func buildCalendar(s Spec, tz *time.Location, dtstamp time.Time) (*ical.Calendar, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//atrium//calendar//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, s.UID)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, dtstamp.UTC())
	ev.Props.SetText(ical.PropSummary, s.Title)
	if s.AllDay {
		ev.Props.SetDate(ical.PropDateTimeStart, s.Start.t)
		ev.Props.SetDate(ical.PropDateTimeEnd, s.End.t)
	} else {
		ev.Props.SetDateTime(ical.PropDateTimeStart, s.Start.t.UTC())
		ev.Props.SetDateTime(ical.PropDateTimeEnd, s.End.t.UTC())
	}
	if s.Location != "" {
		ev.Props.SetText(ical.PropLocation, s.Location)
	}
	if s.Notes != "" {
		ev.Props.SetText(ical.PropDescription, s.Notes)
	}
	cal.Children = append(cal.Children, ev.Component)
	return cal, nil
}
