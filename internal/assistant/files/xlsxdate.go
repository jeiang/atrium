package files

import (
	"fmt"
	"math"
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

func fromISO8601(value string) (string, error) {
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04",
		"2006-01-02",
		"15:04:05.999999999",
		"15:04",
	} {
		t, err := time.Parse(layout, value)
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(layout, "2006") && strings.Contains(layout, "15"):
			return formatDateTime(t), nil
		case strings.Contains(layout, "2006"):
			return t.Format("2006-01-02"), nil
		default:
			return formatClockTime(t), nil
		}
	}
	if strings.HasPrefix(value, "PT") {
		return durationISO(value)
	}
	return "", errValue
}

func formatDateTime(t time.Time) string {
	text := t.Format("2006-01-02 15:04:05")
	if t.Nanosecond() != 0 {
		text += fmt.Sprintf(".%06d", t.Nanosecond()/1000)
	}
	return text
}

func formatClockTime(t time.Time) string {
	text := t.Format("15:04:05")
	if t.Nanosecond() != 0 {
		text += fmt.Sprintf(".%06d", t.Nanosecond()/1000)
	}
	return text
}

func durationISO(value string) (string, error) {
	rest, ok := strings.CutPrefix(value, "PT")
	if !ok {
		return "", errValue
	}
	d, err := time.ParseDuration(strings.ToLower(rest))
	if err != nil {
		return "", errValue
	}
	days := d / (24 * time.Hour)
	remainder := d - days*24*time.Hour
	secs := remainder / time.Second
	us := (remainder % time.Second) / time.Microsecond
	return formatTimedelta(int64(days), int64(secs), int64(us)), nil
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
