package calendar

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/py"
	gcal "google.golang.org/api/calendar/v3"
)

type Spec struct {
	UID      string
	Title    string
	Start    string
	End      string
	AllDay   bool
	Location string
	Notes    string
}

func (s Spec) Dict() py.Dict {
	return py.Dict{
		{Key: "uid", Value: s.UID}, {Key: "title", Value: s.Title}, {Key: "start", Value: s.Start}, {Key: "end", Value: s.End},
		{Key: "all_day", Value: s.AllDay}, {Key: "location", Value: s.Location}, {Key: "notes", Value: s.Notes},
	}
}

func SpecOf(v any) (Spec, error) {
	d, ok := v.(py.Dict)
	if !ok {
		if m, isMap := v.(map[string]any); isMap {
			for k, val := range m {
				d = append(d, py.Pair{Key: k, Value: val})
			}
		} else {
			return Spec{}, fmt.Errorf("%w: spec is %T, not an object", httpx.ErrShape, v)
		}
	}
	var s Spec
	text := func(key string) (string, error) {
		val, found := d.Get(key)
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
	}{{"uid", &s.UID}, {"title", &s.Title}, {"start", &s.Start}, {"end", &s.End}, {"location", &s.Location}, {"notes", &s.Notes}} {
		if *field.dst, err = text(field.key); err != nil {
			return Spec{}, err
		}
	}
	allDay, found := d.Get("all_day")
	if !found {
		return Spec{}, fmt.Errorf("%w: 'all_day'", httpx.ErrMissing)
	}
	s.AllDay, _ = allDay.(bool)
	return s, nil
}

func NewUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}

func textField(raw py.Dict, key string) (string, error) {
	v, _ := raw.Get(key)
	if v == nil || v == "" {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s is %T, not a string", httpx.ErrShape, key, v)
	}
	return strings.TrimSpace(s), nil
}

func NormalizeSpec(raw py.Dict, tz *time.Location, uid string, defaultMinutes int) (Spec, error) {
	title, err := textField(raw, "title")
	if err != nil {
		return Spec{}, err
	}
	if title == "" {
		return Spec{}, Error("title is required")
	}
	flag, _ := raw.Get("all_day")
	allDay, _ := flag.(bool)
	startRaw, _ := raw.Get("start")
	start, err := ParseWhen(startRaw, tz, allDay)
	if err != nil {
		return Spec{}, err
	}
	var end Value
	endRaw, _ := raw.Get("end")
	given, _ := endRaw.(string)
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
		given, _ := raw.Get("uid")
		if s, ok := given.(string); ok {
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
	return Spec{UID: uid, Title: title, Start: start.iso(), End: end.iso(), AllDay: allDay, Location: location, Notes: notes}, nil
}

func (s Spec) bounds(tz *time.Location) (Value, Value, error) {
	start, err := ParseWhen(s.Start, tz, s.AllDay)
	if err != nil {
		return Value{}, Value{}, err
	}
	end, err := ParseWhen(s.End, tz, s.AllDay)
	return start, end, err
}

func GoogleBody(s Spec, tz *time.Location) (*gcal.Event, error) {
	start, end, err := s.bounds(tz)
	if err != nil {
		return nil, err
	}
	body := &gcal.Event{Id: s.UID, Summary: s.Title}
	if s.AllDay {
		body.Start = &gcal.EventDateTime{Date: start.iso()}
		body.End = &gcal.EventDateTime{Date: end.iso()}
	} else {
		zone := tz.String()
		body.Start = &gcal.EventDateTime{DateTime: start.iso(), TimeZone: zone}
		body.End = &gcal.EventDateTime{DateTime: end.iso(), TimeZone: zone}
	}
	body.Location = s.Location
	body.Description = s.Notes
	return body, nil
}

func buildCalendar(s Spec, tz *time.Location, dtstamp time.Time) (*ical.Calendar, error) {
	start, end, err := s.bounds(tz)
	if err != nil {
		return nil, err
	}
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//atrium//calendar//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, s.UID)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, dtstamp.UTC())
	ev.Props.SetText(ical.PropSummary, s.Title)
	if s.AllDay {
		ev.Props.SetDate(ical.PropDateTimeStart, start.t)
		ev.Props.SetDate(ical.PropDateTimeEnd, end.t)
	} else {
		ev.Props.SetDateTime(ical.PropDateTimeStart, start.t.UTC())
		ev.Props.SetDateTime(ical.PropDateTimeEnd, end.t.UTC())
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
