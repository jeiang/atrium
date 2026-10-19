package files

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var builtinFormats = map[int]string{
	0: "General", 1: "0", 2: "0.00", 3: "#,##0", 4: "#,##0.00",
	5: `"$"#,##0_);("$"#,##0)`, 6: `"$"#,##0_);[Red]("$"#,##0)`, 7: `"$"#,##0.00_);("$"#,##0.00)`, 8: `"$"#,##0.00_);[Red]("$"#,##0.00)`,
	9: "0%", 10: "0.00%", 11: "0.00E+00", 12: "# ?/?", 13: "# ??/??",
	14: "mm-dd-yy", 15: "d-mmm-yy", 16: "d-mmm", 17: "mmm-yy", 18: "h:mm AM/PM", 19: "h:mm:ss AM/PM",
	20: "h:mm", 21: "h:mm:ss", 22: "m/d/yy h:mm",
	37: "#,##0_);(#,##0)", 38: "#,##0_);[Red](#,##0)", 39: "#,##0.00_);(#,##0.00)", 40: "#,##0.00_);[Red](#,##0.00)",
	41: `_(* #,##0_);_(* \(#,##0\);_(* "-"_);_(@_)`,
	42: `_("$"* #,##0_);_("$"* \(#,##0\);_("$"* "-"_);_(@_)`,
	43: `_(* #,##0.00_);_(* \(#,##0.00\);_(* "-"??_);_(@_)`,
	44: `_("$"* #,##0.00_)_("$"* \(#,##0.00\)_("$"* "-"??_)_(@_)`,
	45: "mm:ss", 46: "[h]:mm:ss", 47: "mmss.0", 48: "##0.0E+0", 49: "@",
}

func stripLiterals(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '"':
			if j := strings.IndexByte(s[i+1:], '"'); j >= 0 && !strings.Contains(s[i+1:i+1+j], "\n") {
				i += j + 2
				continue
			}
		case '[':
			if !clockBracket(s[i+1:]) {
				if j := strings.IndexByte(s[i+1:], ']'); j >= 0 {
					i += j + 2
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func clockBracket(rest string) bool {
	for _, unit := range []string{"hh]", "h]", "mm]", "m]", "ss]", "s]"} {
		if strings.HasPrefix(rest, unit) {
			return true
		}
	}
	return false
}

func isDateFormat(code string) bool {
	first, _, _ := strings.Cut(code, ";")
	stripped := stripLiterals(first)
	for i := range len(stripped) {
		if !strings.ContainsRune("dmhysDMHYS", rune(stripped[i])) {
			continue
		}
		if i > 0 && (stripped[i-1] == '_' || stripped[i-1] == '\\') {
			continue
		}
		return true
	}
	return false
}

func isTimedeltaFormat(code string) bool {
	first, _, _ := strings.Cut(code, ";")
	lower := strings.ToLower(first)
	for _, unit := range []string{"[h]", "[hh]", "[m]", "[mm]", "[s]", "[ss]"} {
		if strings.Contains(lower, unit) {
			return true
		}
	}
	return false
}

const (
	maxExcelDays = 3_000_000
	msPerDay     = 86_400_000
)

func fromExcel(value float64, date1904, asDelta bool) (string, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", false
	}
	if asDelta {
		return formatDelta(value)
	}
	day := math.Floor(value)
	ms := int64(math.RoundToEven((value - day) * 86400 * 1000))
	if value >= 0 && value < 1 && ms < msPerDay {
		return formatClock(ms), true
	}
	epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if value > 0 && value < 60 {
		day++
	}
	if math.Abs(day) > maxExcelDays {
		return "", false
	}
	t := epoch.AddDate(0, 0, int(day)).Add(time.Duration(ms) * time.Millisecond)
	if t.Year() < 1 || t.Year() > 9999 {
		return "", false
	}
	text := t.Format("2006-01-02 15:04:05")
	if ms%1000 != 0 {
		text += fmt.Sprintf(".%06d", ms%1000*1000)
	}
	return text, true
}

func formatClock(ms int64) string {
	secs := ms / 1000
	text := fmt.Sprintf("%02d:%02d:%02d", secs/3600, secs/60%60, secs%60)
	if micro := ms % 1000 * 1000; micro != 0 {
		text += fmt.Sprintf(".%06d", micro)
	}
	return text
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func formatDelta(value float64) (string, bool) {
	if math.Abs(value) > 999_999_999 {
		return "", false
	}
	whole, fraction := math.Modf(value)
	seconds, secondFraction := math.Modf(fraction * 86400)
	micro := int64(math.RoundToEven(secondFraction * 1e6))
	total := int64(seconds)*1_000_000 + micro
	days := int64(whole) + floorDiv(total, 86_400_000_000)
	total -= floorDiv(total, 86_400_000_000) * 86_400_000_000
	secs, us := total/1_000_000, total%1_000_000
	if us != 0 {
		rounded := int64(math.RoundToEven(float64(us)/1000)) * 1000
		us = rounded
		if us == 1_000_000 {
			secs++
			us = 0
		}
		if secs == 86_400 {
			days++
			secs = 0
		}
	}
	return formatTimedelta(days, secs, us), true
}

func digits(s string, n int) (int, bool) {
	if len(s) < n {
		return 0, false
	}
	for i := range n {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, _ := strconv.Atoi(s[:n])
	return v, true
}

func fromISO8601(value string) (string, error) {
	s := value
	var year, month, day, hour, minute, second, micro int
	hasDate, hasTime := false, false
	if y, ok := digits(s, 4); ok && len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		m, okM := digits(s[5:], 2)
		d, okD := digits(s[8:], 2)
		if okM && okD {
			year, month, day, hasDate = y, m, d, true
			s = s[10:]
		}
	}
	s = strings.TrimPrefix(s, "T")
	if h, ok := digits(s, 2); ok && len(s) >= 5 && s[2] == ':' {
		if mi, ok := digits(s[3:], 2); ok {
			hour, minute, hasTime = h, mi, true
			s = s[5:]
			if len(s) >= 3 && s[0] == ':' {
				if sec, ok := digits(s[1:], 2); ok {
					second = sec
					s = s[3:]
					if len(s) >= 2 && s[0] == '.' {
						n := 1
						for n < len(s) && n <= 3 && s[n] >= '0' && s[n] <= '9' {
							n++
						}
						if n > 1 {
							f, _ := strconv.ParseFloat(s[:n], 64)
							micro = int(f * 1_000_000)
						}
					}
				}
			}
		}
	}
	if !hasDate && !hasTime {
		return durationISO(value)
	}
	switch {
	case hasDate && !hasTime:
		if _, err := validDate(year, month, day); err != nil {
			return "", err
		}
		return fmt.Sprintf("%04d-%02d-%02d", year, month, day), nil
	case !hasDate:
		if hour > 23 || minute > 59 || second > 59 {
			return "", errValue
		}
		text := fmt.Sprintf("%02d:%02d:%02d", hour, minute, second)
		if micro != 0 {
			text += fmt.Sprintf(".%06d", micro)
		}
		return text, nil
	}
	if hour > 23 || minute > 59 || second > 59 {
		return "", errValue
	}
	if _, err := validDate(year, month, day); err != nil {
		return "", err
	}
	text := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", year, month, day, hour, minute, second)
	if micro != 0 {
		text += fmt.Sprintf(".%06d", micro)
	}
	return text, nil
}

func validDate(year, month, day int) (time.Time, error) {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if year < 1 || month < 1 || month > 12 || t.Day() != day || int(t.Month()) != month {
		return t, errValue
	}
	return t, nil
}

func takeDigits(s string) (digits, rest string) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n], s[n:]
}

func takeUnit(s string, unit byte) (digits, rest string, ok bool) {
	d, r := takeDigits(s)
	if d != "" && r != "" && r[0] == unit {
		return d, r[1:], true
	}
	return "", s, false
}

func takeSeconds(s string) (whole, millis, rest string, ok bool) {
	d, r := takeDigits(s)
	if d == "" {
		return "", "", s, false
	}
	if strings.HasPrefix(r, "S") {
		return d, "", r[1:], true
	}
	if strings.HasPrefix(r, ".") {
		frac, after := takeDigits(r[1:])
		if len(frac) >= 1 && len(frac) <= 3 && strings.HasPrefix(after, "S") {
			return d, frac, after[1:], true
		}
	}
	return "", "", s, false
}

var errOverflow = readError("OverflowError")

// durationISO is openpyxl's ISO_DURATION branch of from_ISO8601: PT[nH][nM][n[.fff]S] at the start of the text
// (trailing text is ignored), at least one part, shown as str(timedelta).
func durationISO(value string) (string, error) {
	rest, ok := strings.CutPrefix(value, "PT")
	if !ok {
		return "", errValue
	}
	var hours, minutes, secs, millis int64
	matched := false
	parse := func(digits string) (int64, error) {
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil || n > 1<<40 {
			return 0, errOverflow
		}
		return n, nil
	}
	if d, r, ok := takeUnit(rest, 'H'); ok {
		var err error
		if hours, err = parse(d); err != nil {
			return "", err
		}
		rest, matched = r, true
	}
	if d, r, ok := takeUnit(rest, 'M'); ok {
		var err error
		if minutes, err = parse(d); err != nil {
			return "", err
		}
		rest, matched = r, true
	}
	if whole, frac, _, ok := takeSeconds(rest); ok {
		var err error
		if secs, err = parse(whole); err != nil {
			return "", err
		}
		if frac != "" {
			f, _ := strconv.ParseInt((frac + "00")[:3], 10, 64)
			millis = f
		}
		matched = true
	}
	if !matched {
		return "", errValue
	}
	total := hours*3600 + minutes*60 + secs
	if total/86400 > 999_999_999 {
		return "", errOverflow
	}
	return formatTimedelta(total/86400, total%86400, millis*1000), nil
}

func formatTimedelta(days, secs, us int64) string {
	text := ""
	if days != 0 {
		plural := "s"
		if days == 1 || days == -1 {
			plural = ""
		}
		text = fmt.Sprintf("%d day%s, ", days, plural)
	}
	text += fmt.Sprintf("%d:%02d:%02d", secs/3600, secs/60%60, secs%60)
	if us != 0 {
		text += fmt.Sprintf(".%06d", us)
	}
	return text
}
