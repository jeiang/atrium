package calendar

import (
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/timex"
)

type kind uint8

const (
	kindDate kind = iota
	kindNaive
	kindAware
)

type Value struct {
	kind kind
	t    time.Time
}

func dateValue(y int, m time.Month, d int) Value {
	return Value{kindDate, time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

func naiveValue(wall time.Time) Value { return Value{kindNaive, wall} }

func awareValue(t time.Time) Value { return Value{kindAware, t} }

func (v Value) isDate() bool { return v.kind == kindDate }

func (v Value) isDatetime() bool { return v.kind != kindDate }

func (v Value) isAware() bool { return v.kind == kindAware }

func wall(t time.Time) time.Time {
	y, mo, d := t.Date()
	h, mi, s := t.Clock()
	return time.Date(y, mo, d, h, mi, s, t.Nanosecond(), time.UTC)
}

func resolve(loc *time.Location, w time.Time) time.Time {
	return time.Unix(timex.WallToUnix(loc, w.Unix()), int64(w.Nanosecond())).In(loc)
}

func (v Value) midnight() time.Time {
	y, mo, d := v.t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
}

func asDT(v Value, tz *time.Location) time.Time {
	switch v.kind {
	case kindAware:
		return v.t
	case kindNaive:
		return resolve(tz, v.t)
	}
	return resolve(tz, v.midnight())
}

func naiveLocal(v Value, tz *time.Location) time.Time {
	return wall(asDT(v, tz).In(tz))
}

func addWallDays(t time.Time, days int) time.Time {
	w := wall(t).AddDate(0, 0, days)
	return resolve(t.Location(), w)
}

func after(start Value, exact time.Duration, tz *time.Location, days int) Value {
	if start.isDate() {
		return dateValue(start.t.Year(), start.t.Month(), start.t.Day()+days)
	}
	return awareValue(addWallDays(asDT(start, tz), days).UTC().Add(exact))
}

func isoAuto(t time.Time) string {
	_, off := t.Zone()
	sign := byte('+')
	if off < 0 {
		sign, off = '-', -off
	}
	micro := ""
	if us := t.Nanosecond() / 1000; us != 0 {
		micro = fmt.Sprintf(".%06d", us)
	}
	zone := fmt.Sprintf("%c%02d:%02d", sign, off/3600, off%3600/60)
	if s := off % 60; s != 0 {
		zone += fmt.Sprintf(":%02d", s)
	}
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d%s%s", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), micro, zone)
}

func (v Value) iso() string {
	if v.isDate() {
		return fmt.Sprintf("%04d-%02d-%02d", v.t.Year(), int(v.t.Month()), v.t.Day())
	}
	if v.isAware() {
		return isoAuto(v.t)
	}
	return strings.TrimSuffix(isoAuto(v.t), "+00:00")
}

func fmtValue(v Value, tz *time.Location) string {
	if v.isDatetime() {
		return asDT(v, tz).In(tz).Format(time.RFC3339)
	}
	return v.iso()
}

type isoStamp struct {
	t     time.Time
	aware bool
}

func parseISO(s string) (isoStamp, bool) {
	for _, layout := range []string{
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02T15:04Z07:00",
		"2006-01-02 15:04Z07:00",
		"2006-01-02T15:04:05Z0700",
		"2006-01-02 15:04:05Z0700",
		"2006-01-02T15:04Z0700",
		"2006-01-02 15:04Z0700",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return isoStamp{t: t, aware: true}, true
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return isoStamp{t: t}, true
		}
	}
	return isoStamp{}, false
}

func ParseWhen(raw any, tz *time.Location, allDay bool) (Value, error) {
	text, ok := raw.(string)
	trimmed := strings.TrimSpace(text)
	if !ok || trimmed == "" {
		return Value{}, Error("start and end must be ISO-8601 strings")
	}
	bad := func(msg string) error { return Error(fmt.Sprintf("bad date/time %q: %s", text, msg)) }
	separated := strings.ContainsAny(trimmed, "T ")
	if allDay {
		if separated {
			stamp, ok := parseISO(trimmed)
			if !ok {
				return Value{}, bad("invalid ISO-8601 date/time")
			}
			y, m, d := stamp.t.Date()
			return dateValue(y, m, d), nil
		}
		date, err := time.Parse("2006-01-02", trimmed)
		if err != nil {
			return Value{}, bad("invalid ISO-8601 date")
		}
		y, m, d := date.Date()
		return dateValue(y, m, d), nil
	}
	stamp, ok := parseISO(trimmed)
	if !ok {
		return Value{}, bad("invalid ISO-8601 date/time")
	}
	if !separated {
		y, m, d := stamp.t.Date()
		return awareValue(resolve(tz, time.Date(y, m, d, 0, 0, 0, 0, time.UTC))), nil
	}
	if stamp.aware {
		return awareValue(stamp.t), nil
	}
	return awareValue(resolve(tz, stamp.t)), nil
}
