package scheduler

import (
	"unicode/utf8"
)

type isoDateTime struct {
	year, month, day                  int
	hour, minute, second, microsecond int
	aware                             bool
	offsetSeconds, offsetMicroseconds int
}

type isoBuf []byte

func (b isoBuf) at(i int) byte {
	if i < 0 || i >= len(b) {
		return 0
	}
	return b[i]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (b isoBuf) digits(p int, v *int, n int) (int, bool) {
	for range n {
		c := b.at(p)
		if !isDigit(c) {
			return p, false
		}
		*v = *v*10 + int(c-'0')
		p++
	}
	return p, true
}

// fromISOFormat is CPython 3.14's datetime.fromisoformat (Modules/_datetimemodule.c).
func fromISOFormat(s string) (isoDateTime, bool) {
	var dt isoDateTime
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) < 7 {
		return dt, false
	}
	b := isoBuf(s)
	sep := separatorLocation(b)
	if sep < 0 {
		return dt, false
	}
	rv := b.parseDate(sep, &dt)
	if rv == 0 && len(b) > sep {
		p := sep
		switch {
		case b[p]&0x80 == 0:
			p++
		case b[p]&0xf0 == 0xe0:
			p += 3
		case b[p]&0xf0 == 0xf0:
			p += 4
		default:
			p += 2
		}
		rv = b.parseTime(p, len(b), &dt)
	}
	if rv < 0 {
		return dt, false
	}
	if rv == 1 {
		dt.aware = true
		total := int64(dt.offsetSeconds)*1_000_000 + int64(dt.offsetMicroseconds)
		if total <= -86_400_000_000 || total >= 86_400_000_000 {
			return dt, false
		}
	}
	if dt.hour == 24 && dt.month >= 1 && dt.month <= 12 {
		dim := daysInMonth(dt.year, dt.month)
		if dt.day <= dim {
			if dt.minute != 0 || dt.second != 0 || dt.microsecond != 0 {
				return dt, false
			}
			dt.hour = 0
			dt.day++
			if dt.day > dim {
				dt.day = 1
				dt.month++
				if dt.month > 12 {
					dt.month = 1
					dt.year++
				}
			}
		}
	}
	if dt.year < 1 || dt.year > 9999 || dt.month < 1 || dt.month > 12 || dt.day < 1 || dt.day > daysInMonth(dt.year, dt.month) {
		return dt, false
	}
	if dt.hour > 23 || dt.minute > 59 || dt.second > 59 || dt.microsecond > 999_999 {
		return dt, false
	}
	return dt, true
}

func separatorLocation(b isoBuf) int {
	n := len(b)
	if n == 7 {
		return 7
	}
	if b.at(4) == '-' {
		if b.at(5) != 'W' {
			return 10
		}
		if n < 8 {
			return -1
		}
		if n > 8 && b.at(8) == '-' {
			if n == 9 {
				return -1
			}
			if n > 10 && isDigit(b.at(10)) {
				return 8
			}
			return 10
		}
		return 8
	}
	if b.at(4) != 'W' {
		return 8
	}
	idx := 7
	for ; idx < n; idx++ {
		if !isDigit(b.at(idx)) {
			break
		}
	}
	if idx < 9 {
		return idx
	}
	if idx%2 == 0 {
		return 7
	}
	return 8
}

func (b isoBuf) parseDate(n int, dt *isoDateTime) int {
	p, ok := b.digits(0, &dt.year, 4)
	if !ok {
		return -1
	}
	usesSep := b.at(p) == '-'
	if usesSep {
		p++
	}
	if b.at(p) == 'W' {
		p++
		week, day := 0, 0
		if p, ok = b.digits(p, &week, 2); !ok {
			return -3
		}
		if p < n {
			if usesSep {
				c := b.at(p)
				p++
				if c != '-' {
					return -2
				}
			}
			if _, ok = b.digits(p, &day, 1); !ok {
				return -4
			}
		} else {
			day = 1
		}
		y, m, d, ok := isoToYMD(dt.year, week, day)
		if !ok {
			return -5
		}
		dt.year, dt.month, dt.day = y, m, d
		return 0
	}
	if p, ok = b.digits(p, &dt.month, 2); !ok {
		return -1
	}
	if usesSep {
		c := b.at(p)
		p++
		if c != '-' {
			return -2
		}
	}
	if _, ok = b.digits(p, &dt.day, 2); !ok {
		return -1
	}
	return 0
}

func (b isoBuf) parseHMSF(p, end int, h, m, s, us *int) int {
	*h, *m, *s, *us = 0, 0, 0, 0
	vals := [3]*int{h, m, s}
	hasSep := true
	for i := range 3 {
		var ok bool
		if p, ok = b.digits(p, vals[i], 2); !ok {
			return -3
		}
		c := b.at(p)
		p++
		if i == 0 {
			hasSep = c == ':'
		}
		if c == '.' || c == ',' {
			if i < 2 || p >= end {
				return -3
			}
			break
		} else if p >= end {
			if c != 0 {
				return 1
			}
			return 0
		} else if hasSep && c == ':' {
			if i == 2 {
				return -4
			}
			continue
		} else if !hasSep {
			p--
		} else {
			return -4
		}
	}
	toParse := min(end-p, 6)
	var ok bool
	if p, ok = b.digits(p, us, toParse); !ok {
		return -3
	}
	if toParse < 6 {
		*us *= [...]int{100000, 10000, 1000, 100, 10}[toParse-1]
	}
	for isDigit(b.at(p)) {
		p++
	}
	if b.at(p) != 0 {
		return 1
	}
	return 0
}

func (b isoBuf) parseTime(p0, end int, dt *isoDateTime) int {
	tz := p0
	for {
		if c := b.at(tz); c == 'Z' || c == '+' || c == '-' {
			break
		}
		tz++
		if tz >= end {
			break
		}
	}
	rv := b.parseHMSF(p0, tz, &dt.hour, &dt.minute, &dt.second, &dt.microsecond)
	if rv < 0 {
		return rv
	}
	if tz == end {
		if rv == 1 {
			return -5
		}
		return 0
	}
	if b.at(tz) == 'Z' {
		if b.at(tz+1) != 0 {
			return -5
		}
		return 1
	}
	sign := 1
	if b.at(tz) == '-' {
		sign = -1
	}
	var th, tm, ts, tus int
	rv = b.parseHMSF(tz+1, end, &th, &tm, &ts, &tus)
	dt.offsetSeconds = sign * (th*3600 + tm*60 + ts)
	dt.offsetMicroseconds = sign * tus
	if rv != 0 {
		return -5
	}
	return 1
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

func daysInMonth(y, m int) int {
	if m == 2 && isLeap(y) {
		return 29
	}
	return [...]int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[m]
}

func daysBeforeYear(y int) int {
	y--
	return y*365 + y/4 - y/100 + y/400
}

func daysBeforeMonth(y, m int) int {
	n := 0
	for i := 1; i < m; i++ {
		n += daysInMonth(y, i)
	}
	return n
}

func ymdToOrd(y, m, d int) int { return daysBeforeYear(y) + daysBeforeMonth(y, m) + d }

func ordToYMD(n int) (int, int, int) {
	n--
	n400, n := n/146097, n%146097
	y := n400*400 + 1
	n100, n := n/36524, n%36524
	n4, n := n/1461, n%1461
	n1, n := n/365, n%365
	y += n100*100 + n4*4 + n1
	if n1 == 4 || n100 == 4 {
		return y - 1, 12, 31
	}
	m := 1
	for n >= daysInMonth(y, m) {
		n -= daysInMonth(y, m)
		m++
	}
	return y, m, n + 1
}

func isoToYMD(year, week, day int) (int, int, int, bool) {
	if year < 1 || year > 9999 {
		return 0, 0, 0, false
	}
	if week <= 0 || week >= 53 {
		first := (ymdToOrd(year, 1, 1) + 6) % 7
		longYear := first == 3 || (first == 2 && isLeap(year))
		if week != 53 || !longYear {
			return 0, 0, 0, false
		}
	}
	if day <= 0 || day >= 8 {
		return 0, 0, 0, false
	}
	firstDay := ymdToOrd(year, 1, 1)
	firstWeekday := (firstDay + 6) % 7
	monday := firstDay - firstWeekday
	if firstWeekday > 3 {
		monday += 7
	}
	y, m, d := ordToYMD(monday + (week-1)*7 + day - 1)
	return y, m, d, true
}
