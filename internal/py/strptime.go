package py

import "time"

func number(s string) int {
	n := 0
	for _, c := range []byte(s) {
		n = n*10 + int(c-'0')
	}
	return n
}

// monthTexts are the prefixes of s the regexp alternatives 1[0-2]|0[1-9]|[1-9] match, in that order.
func monthTexts(s string) []string {
	var out []string
	if len(s) >= 2 && isDigit(s[1]) {
		if s[0] == '1' && s[1] <= '2' || s[0] == '0' && s[1] >= '1' {
			out = append(out, s[:2])
		}
	}
	if len(s) >= 1 && s[0] >= '1' && s[0] <= '9' {
		out = append(out, s[:1])
	}
	return out
}

// dayText reports whether s is one of 3[0-1]|[1-2]\d|0[1-9]|[1-9]| [1-9] and its value.
func dayText(s string) (int, bool) {
	switch len(s) {
	case 1:
		return number(s), s[0] >= '1' && s[0] <= '9'
	case 2:
		switch {
		case s[0] == '3':
			return number(s), s[1] == '0' || s[1] == '1'
		case s[0] == '1' || s[0] == '2':
			return number(s), isDigit(s[1])
		case s[0] == '0':
			return number(s[1:]), s[1] >= '1' && s[1] <= '9'
		case s[0] == ' ':
			return number(s[1:]), s[1] >= '1' && s[1] <= '9'
		}
	}
	return 0, false
}

// StrptimeDate is datetime.strptime(value, "%Y-%m-%d"): a four-digit year, a one- or two-digit month and day, and
// nothing after; a day the month lacks fails as ValueError does.
func StrptimeDate(value string) (DateTime, bool) {
	if len(value) < 6 || value[4] != '-' {
		return DateTime{}, false
	}
	for i := range 4 {
		if !isDigit(value[i]) {
			return DateTime{}, false
		}
	}
	year := number(value[:4])
	for _, mtext := range monthTexts(value[5:]) {
		rest := value[5+len(mtext):]
		if rest == "" || rest[0] != '-' {
			continue
		}
		day, ok := dayText(rest[1:])
		if !ok {
			continue
		}
		month := number(mtext)
		if year < 1 || time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Day() != day {
			return DateTime{}, false
		}
		return DateTime{Year: year, Month: month, Day: day}, true
	}
	return DateTime{}, false
}
