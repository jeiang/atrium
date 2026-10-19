package py

import "time"

func ascii(r rune, lo, hi rune) bool { return r >= lo && r <= hi }

// monthTexts are the prefixes of s the alternatives 1[0-2]|0[1-9]|[1-9] match, in that order.
func monthTexts(s []rune) [][]rune {
	var out [][]rune
	if len(s) >= 2 && (s[0] == '1' && ascii(s[1], '0', '2') || s[0] == '0' && ascii(s[1], '1', '9')) {
		out = append(out, s[:2])
	}
	if len(s) >= 1 && ascii(s[0], '1', '9') {
		out = append(out, s[:1])
	}
	return out
}

// dayText reports whether s is one of 3[0-1]|[1-2]\d|0[1-9]|[1-9]| [1-9] and its value.
func dayText(s []rune) (int, bool) {
	switch len(s) {
	case 1:
		return DigitValue(s[0]), ascii(s[0], '1', '9')
	case 2:
		switch {
		case s[0] == '3':
			return DigitValue(s[1]) + 30, s[1] == '0' || s[1] == '1'
		case s[0] == '1' || s[0] == '2':
			return DigitValue(s[0])*10 + DigitValue(s[1]), IsDecimal(s[1])
		case s[0] == '0', s[0] == ' ':
			return DigitValue(s[1]), ascii(s[1], '1', '9')
		}
	}
	return 0, false
}

// StrptimeDate is datetime.strptime(value, "%Y-%m-%d"): a four-digit year (\d, so any Unicode decimal digit), a
// one- or two-digit month and day, and nothing after; a day the month lacks fails as ValueError does.
func StrptimeDate(value string) (DateTime, bool) {
	r := []rune(value)
	if len(r) < 6 || r[4] != '-' {
		return DateTime{}, false
	}
	year := 0
	for _, c := range r[:4] {
		if !IsDecimal(c) {
			return DateTime{}, false
		}
		year = year*10 + DigitValue(c)
	}
	for _, mtext := range monthTexts(r[5:]) {
		rest := r[5+len(mtext):]
		if len(rest) == 0 || rest[0] != '-' {
			continue
		}
		day, ok := dayText(rest[1:])
		if !ok {
			continue
		}
		month := 0
		for _, c := range mtext {
			month = month*10 + DigitValue(c)
		}
		if year < 1 || time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Day() != day {
			return DateTime{}, false
		}
		return DateTime{Year: year, Month: month, Day: day}, true
	}
	return DateTime{}, false
}
