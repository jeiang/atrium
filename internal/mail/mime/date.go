package mime

import (
	"fmt"
	"slices"
	"time"
)

var monthNames = []string{
	"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec",
	"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december",
}

var dayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

var zoneNames = map[string]int64{
	"UT": 0, "UTC": 0, "GMT": 0, "Z": 0,
	"AST": -400, "ADT": -300, "EST": -500, "EDT": -400, "CST": -600, "CDT": -500,
	"MST": -700, "MDT": -600, "PST": -800, "PDT": -700,
}

type pyDate struct {
	t     time.Time
	naive bool
}

func monthIndex(s []rune) int {
	return slices.Index(monthNames, string(s))
}
func upperRunes(s []rune) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		case r == 0x131:
			r = 'I'
		case r == 0x17F:
			r = 'S'
		case r == 0xFB05 || r == 0xFB06:
			out = append(out, 'S')
			r = 'T'
		}
		out = append(out, r)
	}
	return string(out)
}

func lastRune(s []rune) rune { return s[len(s)-1] }

func parseDate(v []rune) (pyDate, bool) {
	data := splitSpace(v)
	if len(data) == 0 {
		return pyDate{}, false
	}
	if lastRune(data[0]) == ',' || slices.Contains(dayNames, string(lowerRunes(data[0]))) {
		data = data[1:]
	} else if i := lastIndexRune(data[0], ','); i >= 0 {
		data[0] = data[0][i+1:]
	}
	if len(data) == 3 {
		if stuff := splitRune(data[0], '-'); len(stuff) == 3 {
			data = append(stuff, data[1:]...)
		}
	}
	if len(data) == 4 {
		s := data[3]
		i := indexRune(s, '+')
		if i == -1 {
			i = indexRune(s, '-')
		}
		if i > 0 {
			data = append(data[:3:3], s[:i], s[i:])
		} else {
			data = append(data, nil)
		}
	}
	if len(data) < 5 {
		return pyDate{}, false
	}
	dd, mm, yy, tm, tz := data[0], data[1], data[2], data[3], data[4]
	if len(dd) == 0 || len(mm) == 0 || len(yy) == 0 {
		return pyDate{}, false
	}
	mm = lowerRunes(mm)
	if monthIndex(mm) < 0 {
		dd, mm = mm, lowerRunes(dd)
		if monthIndex(mm) < 0 {
			return pyDate{}, false
		}
	}
	month := monthIndex(mm) + 1
	if month > 12 {
		month -= 12
	}
	if lastRune(dd) == ',' {
		dd = dd[:len(dd)-1]
	}
	if i := indexRune(yy, ':'); i > 0 {
		yy, tm = tm, yy
	}
	if lastRune(yy) == ',' {
		yy = yy[:len(yy)-1]
		if len(yy) == 0 {
			return pyDate{}, false
		}
	}
	if !isDigit(yy[0]) {
		yy, tz = tz, yy
	}
	if lastRune(tm) == ',' {
		tm = tm[:len(tm)-1]
	}
	var thh, tmm, tss []rune
	parts := splitRune(tm, ':')
	switch {
	case len(parts) == 2:
		thh, tmm, tss = parts[0], parts[1], []rune("0")
	case len(parts) == 3:
		thh, tmm, tss = parts[0], parts[1], parts[2]
	case len(parts) == 1 && indexRune(parts[0], '.') >= 0:
		dot := splitRune(parts[0], '.')
		switch len(dot) {
		case 2:
			thh, tmm, tss = dot[0], dot[1], []rune("0")
		case 3:
			thh, tmm, tss = dot[0], dot[1], dot[2]
		default:
			return pyDate{}, false
		}
	default:
		return pyDate{}, false
	}
	year, ok1 := pyInt(yy)
	day, ok2 := pyInt(dd)
	hour, ok3 := pyInt(thh)
	minute, ok4 := pyInt(tmm)
	second, ok5 := pyInt(tss)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return pyDate{}, false
	}
	if year < 100 {
		if year > 68 {
			year += 1900
		} else {
			year += 2000
		}
	}
	var offset int64
	hasOffset := false
	zone := upperRunes(tz)
	if z, ok := zoneNames[zone]; ok {
		offset, hasOffset = z, true
	} else if n, ok := pyInt([]rune(zone)); ok {
		offset, hasOffset = n, true
		if n == 0 && len(zone) > 0 && zone[0] == '-' {
			hasOffset = false
		}
	}
	if hasOffset && offset != 0 {
		sign := int64(1)
		if offset < 0 {
			sign, offset = -1, -offset
		}
		offset = sign * ((offset/100)*3600 + (offset%100)*60)
	}
	if year < 1 || year > 9999 || day < 1 || day > int64(daysIn(month, year)) ||
		hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 59 {
		return pyDate{}, false
	}
	if !hasOffset {
		return pyDate{t: time.Date(int(year), time.Month(month), int(day), int(hour), int(minute), int(second), 0, time.UTC), naive: true}, true
	}
	if offset <= -86400 || offset >= 86400 {
		return pyDate{}, false
	}
	loc := time.FixedZone("", int(offset))
	return pyDate{t: time.Date(int(year), time.Month(month), int(day), int(hour), int(minute), int(second), 0, loc)}, true
}

func daysIn(month int, year int64) int {
	return time.Date(int(year), time.Month(month+1), 0, 0, 0, 0, 0, time.UTC).Day()
}

func lastIndexRune(s []rune, r rune) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == r {
			return i
		}
	}
	return -1
}

func splitRune(s []rune, sep rune) [][]rune {
	var out [][]rune
	start := 0
	for i, r := range s {
		if r == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func (d pyDate) format() string {
	zone := "-0000"
	if !d.naive {
		_, off := d.t.Zone()
		sign := '+'
		if off < 0 {
			sign, off = '-', -off
		}
		zone = fmt.Sprintf("%c%02d%02d", sign, off/3600, off%3600/60)
	}
	return fmt.Sprintf("%s, %02d %s %04d %02d:%02d:%02d %s", d.t.Weekday().String()[:3], d.t.Day(), d.t.Month().String()[:3],
		d.t.Year(), d.t.Hour(), d.t.Minute(), d.t.Second(), zone)
}

// Timestamp is Python's email_ts rule: the POSIX time of the date, with a missing or -0000 zone read as UTC.
func (d pyDate) Timestamp() float64 { return float64(d.t.Unix()) }

// ParseDate is email.utils.parsedate_to_datetime followed by features.email_ts: the POSIX time of an RFC 5322
// date, with no zone or -0000 read as UTC. ok is false where Python raises.
func ParseDate(s string) (float64, bool) {
	d, ok := parseDate([]rune(s))
	if !ok {
		return 0, false
	}
	return d.Timestamp(), true
}
