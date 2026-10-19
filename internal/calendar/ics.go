package calendar

import (
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jeiang/atrium/internal/py"
)

// Row is one listed event.
type Row struct {
	UID      string
	Calendar string
	Start    string
	End      string
	Title    string
	Location string
}

// Dict is the row as Python lists it.
func (r Row) Dict() py.Dict {
	return py.Dict{
		{Key: "uid", Value: r.UID}, {Key: "calendar", Value: r.Calendar}, {Key: "start", Value: r.Start},
		{Key: "end", Value: r.End}, {Key: "title", Value: r.Title}, {Key: "location", Value: r.Location},
	}
}

type icsEvent struct {
	uid          *string
	status       string
	summary      string
	location     string
	description  string
	dtstart      *Value
	dtend        *Value
	duration     *time.Duration
	rrules       []string
	rdates       []Value
	hasRDate     bool
	exdates      []Value
	recurrenceID *Value
}

func (e *icsEvent) uidKey() string {
	if e.uid == nil {
		return ""
	}
	return *e.uid
}

func (e *icsEvent) uidText() string {
	if e.uid == nil {
		return "None"
	}
	return *e.uid
}

type valueError string

func (e valueError) Error() string { return string(e) }

func (e valueError) PyClass() string { return "ValueError" }

func unescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch c := s[i+1]; c {
			case 'n', 'N':
				b.WriteByte('\n')
				i++
				continue
			case '\\', ',', ';', ':':
				b.WriteByte(c)
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func digitsAt(s string, from, to int) (int, bool) {
	if to > len(s) || from >= to {
		return 0, false
	}
	n := 0
	for _, c := range []byte(s[from:to]) {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func clockValue(s string, loc *time.Location) (Value, bool) {
	text := s[strings.LastIndexByte(s, ':')+1:]
	if len(text) < 15 || text[8] != 'T' {
		return Value{}, false
	}
	fields := [6]int{}
	for i, span := range [6][2]int{{0, 4}, {4, 6}, {6, 8}, {9, 11}, {11, 13}, {13, 15}} {
		n, ok := digitsAt(text, span[0], span[1])
		if !ok {
			return Value{}, false
		}
		fields[i] = n
	}
	y, mo, d, h, mi, sec := fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]
	if y < 1 || mo < 1 || mo > 12 || d < 1 || d > time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC).Day() || h > 23 || mi > 59 || sec > 59 {
		return Value{}, false
	}
	w := time.Date(y, time.Month(mo), d, h, mi, sec, 0, time.UTC)
	switch {
	case loc != nil:
		return awareValue(resolve(loc, w)), true
	case text[15:] == "":
		return naiveValue(w), true
	case text[15:] == "Z":
		return awareValue(w), true
	}
	return Value{}, false
}

type zones struct {
	custom map[string]*time.Location
}

func (z *zones) lookup(name string) *time.Location {
	clean := strings.Trim(name, "/")
	if loc := zoneNamed(clean); loc != nil {
		return loc
	}
	if olson, ok := windowsToOlson[clean]; ok {
		if loc := zoneNamed(olson); loc != nil {
			return loc
		}
	}
	if loc := zoneNamed(name); loc != nil {
		return loc
	}
	return z.custom[clean]
}

func (z *zones) value(prop ical.Prop) (Value, error) {
	text := prop.Value
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "P") || strings.HasPrefix(upper, "-P") || strings.HasPrefix(upper, "+P") || strings.Contains(upper, "/") {
		return Value{}, valueError("Expected datetime, date, or time. Got: '" + text + "'")
	}
	var loc *time.Location
	if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
		loc = z.lookup(tzid)
	}
	switch len(text) {
	case 15, 16:
		if v, ok := clockValue(text, loc); ok {
			return v, nil
		}
		return Value{}, valueError("Wrong datetime format: " + text)
	case 8:
		y, ok1 := digitsAt(text, 0, 4)
		mo, ok2 := digitsAt(text, 4, 6)
		d, ok3 := digitsAt(text, 6, 8)
		if !ok1 || !ok2 || !ok3 || y < 1 || mo < 1 || mo > 12 || d < 1 || d > time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
			return Value{}, valueError("Wrong date format " + text)
		}
		if loc != nil {
			return awareValue(resolve(loc, time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC))), nil
		}
		return dateValue(y, time.Month(mo), d), nil
	}
	return Value{}, valueError("Expected datetime, date, or time. Got: '" + text + "'")
}

func (z *zones) list(prop ical.Prop) ([]Value, error) {
	var out []Value
	for _, item := range strings.Split(prop.Value, ",") {
		one := prop
		one.Value = item
		v, err := z.value(one)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func parseDuration(s string) (time.Duration, bool) {
	sign := time.Duration(1)
	switch {
	case strings.HasPrefix(s, "-"):
		sign, s = -1, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return 0, false
	}
	s = s[1:]
	var total time.Duration
	take := func(unit byte, scale time.Duration) bool {
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 || i >= len(s) || s[i] != unit {
			return false
		}
		n, ok := py.ParseInt(s[:i])
		if !ok {
			return false
		}
		total += time.Duration(n) * scale
		s = s[i+1:]
		return true
	}
	take('W', 7*24*time.Hour)
	take('D', 24*time.Hour)
	if strings.HasPrefix(s, "T") {
		s = s[1:]
		take('H', time.Hour)
		take('M', time.Minute)
		take('S', time.Second)
	}
	if s != "" {
		return 0, false
	}
	return sign * total, true
}

func parseEvent(comp *ical.Component, z *zones) (*icsEvent, error) {
	ev := &icsEvent{}
	if p := comp.Props.Get(ical.PropUID); p != nil && p.Value != "" {
		uid := unescapeText(p.Value)
		ev.uid = &uid
	} else if p != nil {
		empty := ""
		ev.uid = &empty
	}
	if p := comp.Props.Get(ical.PropStatus); p != nil {
		ev.status = unescapeText(p.Value)
	}
	if p := comp.Props.Get(ical.PropSummary); p != nil {
		ev.summary = unescapeText(p.Value)
	}
	if p := comp.Props.Get(ical.PropLocation); p != nil {
		ev.location = unescapeText(p.Value)
	}
	if p := comp.Props.Get(ical.PropDescription); p != nil {
		ev.description = unescapeText(p.Value)
	}
	single := func(name string) (*Value, error) {
		p := comp.Props.Get(name)
		if p == nil {
			return nil, nil
		}
		v, err := z.value(*p)
		if err != nil {
			return nil, err
		}
		return &v, nil
	}
	var err error
	if ev.dtstart, err = single(ical.PropDateTimeStart); err != nil {
		return nil, err
	}
	if ev.dtend, err = single(ical.PropDateTimeEnd); err != nil {
		return nil, err
	}
	if ev.recurrenceID, err = single(ical.PropRecurrenceID); err != nil {
		return nil, err
	}
	if p := comp.Props.Get(ical.PropDuration); p != nil {
		d, ok := parseDuration(p.Value)
		if !ok {
			return nil, valueError("Invalid iCalendar duration: " + p.Value)
		}
		ev.duration = &d
	}
	for _, p := range comp.Props.Values(ical.PropRecurrenceRule) {
		ev.rrules = append(ev.rrules, p.Value)
	}
	for _, p := range comp.Props.Values(ical.PropRecurrenceDates) {
		ev.hasRDate = true
		values, err := z.list(p)
		if err != nil {
			return nil, err
		}
		ev.rdates = append(ev.rdates, values...)
	}
	for _, p := range comp.Props.Values(ical.PropExceptionDates) {
		values, err := z.list(p)
		if err != nil {
			return nil, err
		}
		ev.exdates = append(ev.exdates, values...)
	}
	return ev, nil
}

func decodeICS(data []byte) (cal *ical.Calendar, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = valueError(fmt.Sprint(r))
		}
	}()
	cal, err = ical.NewDecoder(strings.NewReader(string(data))).Decode()
	if err != nil {
		return nil, valueError(err.Error())
	}
	return cal, nil
}

func walkEvents(comp *ical.Component, z *zones, out *[]*icsEvent) error {
	for _, child := range comp.Children {
		switch child.Name {
		case ical.CompTimezone:
			registerZone(child, z)
		case ical.CompEvent:
			ev, err := parseEvent(child, z)
			if err != nil {
				return err
			}
			*out = append(*out, ev)
		}
		if err := walkEvents(child, z, out); err != nil {
			return err
		}
	}
	return nil
}

func parseICS(data []byte) ([]*icsEvent, error) {
	cal, err := decodeICS(data)
	if err != nil {
		return nil, err
	}
	return parseEvents(cal)
}

func endOf(ev *icsEvent, start Value, tz *time.Location) (Value, error) {
	if ev.dtend != nil {
		return *ev.dtend, nil
	}
	if ev.duration != nil {
		span := *ev.duration
		days := floorDays(span)
		return after(start, span-time.Duration(days)*24*time.Hour, tz, days), nil
	}
	if start.isDate() {
		return dateValue(start.t.Year(), start.t.Month(), start.t.Day()+1), nil
	}
	return start, nil
}

func floorDays(d time.Duration) int {
	day := 24 * time.Hour
	q := d / day
	if d%day < 0 {
		q--
	}
	return int(q)
}

func occurrenceEnd(ev *icsEvent, start, end, occ Value, tz *time.Location) (Value, error) {
	if ev.dtend == nil {
		return endOf(ev, occ, tz)
	}
	if occ.isDate() {
		if !end.isDate() || !start.isDate() {
			return Value{}, py.TypeError("unsupported operand type(s) for -: 'datetime.datetime' and 'datetime.date'")
		}
		return dateValue(occ.t.Year(), occ.t.Month(), occ.t.Day()+int(end.t.Sub(start.t).Hours()/24)), nil
	}
	return after(occ, asDT(end, tz).UTC().Sub(asDT(start, tz).UTC()), tz, 0), nil
}

func longestSpan(master *icsEvent, start, end Value, tz *time.Location) (time.Duration, error) {
	if !start.isDatetime() {
		if !end.isDate() {
			return 0, py.TypeError("unsupported operand type(s) for -: 'datetime.datetime' and 'datetime.date'")
		}
		return naiveLocal(end, tz).Sub(naiveLocal(start, tz)), nil
	}
	if master.dtend == nil && master.duration != nil {
		span := *master.duration
		if floorDays(span) != 0 {
			return span + 2*time.Hour, nil
		}
		return span, nil
	}
	return asDT(end, tz).UTC().Sub(asDT(start, tz).UTC()), nil
}

func overlaps(s, e, ws, we time.Time) bool {
	return s.Before(we) && (e.After(ws) || !s.Before(ws))
}

func (ev *icsEvent) row(calendar string, start, end Value, tz *time.Location) Row {
	return Row{UID: ev.uidKey(), Calendar: calendar, Start: fmtValue(start, tz), End: fmtValue(end, tz), Title: ev.summary, Location: ev.location}
}

// EventsInICS is events_in_ics: the events of one iCalendar file that overlap the window, recurrences expanded.
// Notes collects what could not be expanded.
func EventsInICS(data []byte, calendar string, ws, we time.Time, tz *time.Location, notes *[]string) ([]Row, error) {
	events, err := parseICS(data)
	if err != nil {
		return nil, err
	}
	var masterOrder, overrideOrder []string
	masters := map[string]*icsEvent{}
	overrides := map[string][]*icsEvent{}
	for _, ev := range events {
		uid := ev.uidKey()
		switch {
		case ev.recurrenceID != nil:
			if _, seen := overrides[uid]; !seen {
				overrideOrder = append(overrideOrder, uid)
			}
			overrides[uid] = append(overrides[uid], ev)
		case py.Upper(ev.status) != "CANCELLED":
			if _, seen := masters[uid]; !seen {
				masterOrder = append(masterOrder, uid)
			}
			masters[uid] = ev
		}
	}
	var out []Row
	for _, uid := range masterOrder {
		comp := masters[uid]
		if comp.dtstart == nil {
			continue
		}
		start := *comp.dtstart
		end, err := endOf(comp, start, tz)
		if err != nil {
			return nil, err
		}
		skip := map[int64]struct{}{}
		for _, o := range overrides[uid] {
			skip[asDT(*o.recurrenceID, tz).Unix()] = struct{}{}
		}
		occs, err := occurrences(comp, start, ws, we, tz, notes)
		if err != nil {
			return nil, err
		}
		for _, occ := range occs {
			if _, hidden := skip[asDT(occ, tz).Unix()]; hidden {
				continue
			}
			occEnd, err := occurrenceEnd(comp, start, end, occ, tz)
			if err != nil {
				return nil, err
			}
			if overlaps(asDT(occ, tz), asDT(occEnd, tz), ws, we) {
				out = append(out, comp.row(calendar, occ, occEnd, tz))
			}
		}
	}
	for _, uid := range overrideOrder {
		for _, comp := range overrides[uid] {
			if comp.dtstart == nil || py.Upper(comp.status) == "CANCELLED" {
				continue
			}
			start := *comp.dtstart
			end, err := endOf(comp, start, tz)
			if err != nil {
				return nil, err
			}
			if overlaps(asDT(start, tz), asDT(end, tz), ws, we) {
				out = append(out, comp.row(calendar, start, end, tz))
			}
		}
	}
	return out, nil
}
