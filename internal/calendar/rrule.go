package calendar

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/py"
	"github.com/teambition/rrule-go"
)

var frequencies = map[string]rrule.Frequency{
	"YEARLY": rrule.YEARLY, "MONTHLY": rrule.MONTHLY, "WEEKLY": rrule.WEEKLY, "DAILY": rrule.DAILY,
	"HOURLY": rrule.HOURLY, "MINUTELY": rrule.MINUTELY, "SECONDLY": rrule.SECONDLY,
}

var weekdays = map[string]rrule.Weekday{
	"MO": rrule.MO, "TU": rrule.TU, "WE": rrule.WE, "TH": rrule.TH, "FR": rrule.FR, "SA": rrule.SA, "SU": rrule.SU,
}

type ruleError string

func (e ruleError) Error() string { return string(e) }

func (e ruleError) PyClass() string { return "ValueError" }

func invalid(name, value string) error {
	return ruleError(fmt.Sprintf("invalid %s: %s", py.Repr(name), value))
}

type parsedRule struct {
	opt      rrule.ROption
	until    time.Time
	hasUntil bool
	untilUTC bool
	hasFreq  bool
	empty    bool
}

func intList(value string) ([]int, bool) {
	var out []int
	for _, part := range strings.Split(value, ",") {
		n, ok := py.ParseInt(part)
		if !ok {
			return nil, false
		}
		out = append(out, int(n))
	}
	return out, true
}

func parseUntil(value string) (time.Time, bool, bool) {
	text := value
	utc := strings.HasSuffix(text, "Z")
	text = strings.TrimSuffix(text, "Z")
	layout := "20060102T150405"
	if len(text) == 8 {
		layout = "20060102"
	}
	t, err := time.Parse(layout, text)
	if err != nil {
		return time.Time{}, false, false
	}
	return t, utc, true
}

func parseByDay(value string) ([]rrule.Weekday, error) {
	var out []rrule.Weekday
	for _, item := range strings.Split(value, ",") {
		if item == "" {
			return nil, ruleError("Invalid (empty) BYDAY specification.")
		}
		var w string
		var n int
		if open := strings.IndexByte(item, '('); open >= 0 {
			w = item[:open]
			rest := item[open+1:]
			if len(rest) == 0 {
				return nil, invalid("BYDAY", value)
			}
			parsed, ok := py.ParseInt(rest[:len(rest)-1])
			if !ok {
				return nil, invalid("BYDAY", value)
			}
			n = int(parsed)
			if n == 0 {
				return nil, invalid("BYDAY", value)
			}
		} else {
			i := 0
			for i < len(item) && strings.IndexByte("+-0123456789", item[i]) >= 0 {
				i++
			}
			if i == len(item) {
				return nil, invalid("BYDAY", value)
			}
			w = item[i:]
			if i > 0 {
				parsed, ok := py.ParseInt(item[:i])
				if !ok || parsed == 0 {
					return nil, invalid("BYDAY", value)
				}
				n = int(parsed)
			}
		}
		day, ok := weekdays[w]
		if !ok {
			return nil, invalid("BYDAY", value)
		}
		if n != 0 {
			day = day.Nth(n)
		}
		out = append(out, day)
	}
	return out, nil
}

func parseRule(text string) (*parsedRule, error) {
	text = strings.ToUpper(text)
	if strings.TrimSpace(text) == "" {
		return nil, ruleError("empty string")
	}
	value := text
	if strings.Contains(text, ":") {
		name, rest, _ := strings.Cut(text, ":")
		if strings.Contains(rest, ":") || name != "RRULE" {
			return nil, ruleError("unknown parameter name")
		}
		value = rest
	}
	rule := &parsedRule{opt: rrule.ROption{Wkst: rrule.MO}}
	for _, pair := range strings.Split(value, ";") {
		name, val, ok := strings.Cut(pair, "=")
		if !ok || strings.Contains(val, "=") {
			return nil, ruleError("not enough values to unpack (expected 2, got 1)")
		}
		var good = true
		switch name {
		case "FREQ":
			rule.opt.Freq, good = frequencies[val]
			rule.hasFreq = good
		case "COUNT", "INTERVAL":
			n, ok := py.ParseInt(val)
			good = ok
			if name == "COUNT" {
				rule.opt.Count = int(n)
				rule.empty = n <= 0
			} else {
				rule.opt.Interval = int(n)
			}
		case "BYSETPOS":
			rule.opt.Bysetpos, good = intList(val)
		case "BYMONTH":
			rule.opt.Bymonth, good = intList(val)
		case "BYMONTHDAY":
			rule.opt.Bymonthday, good = intList(val)
		case "BYYEARDAY":
			rule.opt.Byyearday, good = intList(val)
		case "BYEASTER":
			rule.opt.Byeaster, good = intList(val)
		case "BYWEEKNO":
			rule.opt.Byweekno, good = intList(val)
		case "BYHOUR":
			rule.opt.Byhour, good = intList(val)
		case "BYMINUTE":
			rule.opt.Byminute, good = intList(val)
		case "BYSECOND":
			rule.opt.Bysecond, good = intList(val)
		case "BYDAY", "BYWEEKDAY":
			days, err := parseByDay(val)
			if err != nil {
				return nil, err
			}
			rule.opt.Byweekday = days
		case "WKST":
			rule.opt.Wkst, good = weekdays[val]
		case "UNTIL":
			rule.until, rule.untilUTC, good = parseUntil(val)
			rule.hasUntil = good
		default:
			return nil, ruleError(fmt.Sprintf("unknown parameter '%s'", name))
		}
		if !good {
			return nil, invalid(name, val)
		}
	}
	if !rule.hasFreq {
		return nil, ruleError("rrule.__init__() missing 1 required positional argument: 'freq'")
	}
	return rule, nil
}

type source struct {
	next func() (time.Time, bool)
	cur  time.Time
	ok   bool
}

func (s *source) advance() { s.cur, s.ok = s.next() }

func listSource(items []time.Time) *source {
	i := 0
	s := &source{next: func() (time.Time, bool) {
		if i >= len(items) {
			return time.Time{}, false
		}
		i++
		return items[i-1], true
	}}
	s.advance()
	return s
}

func ruleSource(r *rrule.RRule, convert func(time.Time) time.Time) *source {
	next := r.Iterator()
	s := &source{next: func() (time.Time, bool) {
		w, ok := next()
		if !ok {
			return time.Time{}, false
		}
		return convert(w), true
	}}
	s.advance()
	return s
}

func mixError(dts, v Value) error {
	if v.isDatetime() {
		return ruleError("can't compare offset-naive and offset-aware datetimes")
	}
	return ruleError("can't compare datetime.datetime to datetime.date")
}

func occurrences(m *icsEvent, start Value, ws, we time.Time, tz *time.Location, notes *[]string) ([]Value, error) {
	if len(m.rrules) == 0 && !m.hasRDate {
		return []Value{start}, nil
	}
	end, err := endOf(m, start, tz)
	if err != nil {
		return nil, err
	}
	pad, err := longestSpan(m, start, end, tz)
	if err != nil {
		return nil, err
	}
	found, err := expand(m, start, pad, ws, we, tz)
	var re ruleError
	if errors.As(err, &re) {
		*notes = append(*notes, fmt.Sprintf("recurrence of %s not expanded: %s", m.uidText(), re.Error()))
		return []Value{start}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(found) > MaxOccurrences {
		*notes = append(*notes, fmt.Sprintf("%s: more than %d occurrences in window", m.uidText(), MaxOccurrences))
		found = found[:MaxOccurrences]
	}
	if start.isDatetime() {
		return found, nil
	}
	out := make([]Value, len(found))
	for i, v := range found {
		out[i] = dateValue(v.t.Year(), v.t.Month(), v.t.Day())
	}
	return out, nil
}

func expand(m *icsEvent, start Value, pad time.Duration, ws, we time.Time, tz *time.Location) (found []Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			found, err = nil, ruleError(fmt.Sprint(r))
		}
	}()
	aware := start.isAware()
	loc := time.UTC
	dtWall := wall(start.t)
	if aware {
		loc = start.t.Location()
		dtWall = wall(start.t.In(loc))
	}
	dtWall = dtWall.Truncate(time.Second)
	kind := kindNaive
	convert := func(w time.Time) time.Time { return w }
	if aware {
		kind = kindAware
		convert = func(w time.Time) time.Time { return resolve(loc, w) }
	}
	var sources []*source
	for _, text := range m.rrules {
		rule, err := parseRule(text)
		if err != nil {
			return nil, err
		}
		if rule.empty {
			continue
		}
		rule.opt.Dtstart = dtWall
		if rule.hasUntil {
			if aware != rule.untilUTC {
				return nil, ruleError("RRULE UNTIL values must be specified in UTC when DTSTART is timezone-aware")
			}
			rule.opt.Until = rule.until
			if aware {
				rule.opt.Until = wall(rule.until.In(loc))
			}
		}
		r, err := rrule.NewRRule(rule.opt)
		if err != nil {
			return nil, ruleError(err.Error())
		}
		sources = append(sources, ruleSource(r, convert))
	}
	rdates, mixed := conformAll(m.rdates, aware, tz)
	exdates, exMixed := conformAll(m.exdates, aware, tz)
	if mixed == nil {
		mixed = exMixed
	}
	excluded := map[int64]struct{}{}
	for _, t := range exdates {
		excluded[t.Unix()] = struct{}{}
	}
	slices.SortFunc(rdates, func(a, b time.Time) int { return a.Compare(b) })
	sources = append(sources, listSource(rdates))
	var lo, hi time.Time
	switch {
	case aware:
		lo, hi = ws.UTC().Add(-pad), we.UTC()
	case start.isDatetime():
		lo, hi = naiveLocal(awareValue(ws.UTC().Add(-pad)), tz), naiveLocal(awareValue(we), tz)
	default:
		lo, hi = naiveLocal(awareValue(ws), tz).Add(-pad), naiveLocal(awareValue(we), tz)
	}
	if mixed != nil && slices.ContainsFunc(sources, func(s *source) bool { return s.ok }) {
		return nil, mixed
	}
	var out []Value
	started, haveLast := false, false
	var last time.Time
	for {
		var pick *source
		for _, s := range sources {
			if s.ok && (pick == nil || s.cur.Before(pick.cur)) {
				pick = s
			}
		}
		if pick == nil {
			break
		}
		cur := pick.cur
		pick.advance()
		if haveLast && last.Equal(cur) {
			continue
		}
		if _, gone := excluded[cur.Unix()]; gone {
			continue
		}
		last, haveLast = cur, true
		if cur.After(hi) {
			break
		}
		if !started {
			if cur.Before(lo) {
				continue
			}
			started = true
		}
		out = append(out, Value{kind, cur})
	}
	return out, nil
}

func conformAll(values []Value, aware bool, tz *time.Location) ([]time.Time, error) {
	out := make([]time.Time, 0, len(values))
	for _, v := range values {
		if !aware {
			out = append(out, naiveLocal(v, tz))
			continue
		}
		if !v.isAware() {
			return nil, mixError(Value{}, v)
		}
		out = append(out, v.t)
	}
	return out, nil
}
