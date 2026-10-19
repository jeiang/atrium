package py

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FormatFixed is format(x, ".<prec>f").
func FormatFixed(x float64, prec int) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	return strconv.FormatFloat(x, 'f', prec, 64)
}

// FormatPercent is format(x, ".<prec>%"): x times 100 in fixed notation and a percent sign.
func FormatPercent(x float64, prec int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return FormatFixed(x, prec) + "%"
	}
	return FormatFixed(float64(x*100), prec) + "%"
}

// ParseFloat is float(s) for a str: surrounding whitespace is stripped, single underscores may separate
// digits, and "inf", "infinity" and "nan" are accepted in any case. Hexadecimal forms are refused.
// A literal too large for float64 gives an infinity, as in Python.
func ParseFloat(s string) (float64, error) {
	text := Strip(s)
	if !floatSyntax(text) {
		return 0, fmt.Errorf("could not convert string to float: %s", Repr(s))
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(text, "_", ""), 64)
	if err != nil && !math.IsInf(f, 0) {
		return 0, fmt.Errorf("could not convert string to float: %s", Repr(s))
	}
	return f, nil
}

func floatSyntax(s string) bool {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	switch strings.ToLower(s) {
	case "inf", "infinity", "nan":
		return true
	}
	rest, intDigits := digitRun(s)
	frac := 0
	if strings.HasPrefix(rest, ".") {
		rest, frac = digitRun(rest[1:])
	}
	if intDigits < 0 || frac < 0 || intDigits+frac == 0 {
		return false
	}
	if rest == "" {
		return true
	}
	if rest[0] != 'e' && rest[0] != 'E' {
		return false
	}
	rest = rest[1:]
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		rest = rest[1:]
	}
	rest, exp := digitRun(rest)
	return rest == "" && exp > 0
}

// digitRun consumes digits with single underscores between them and returns the rest and the digit count,
// or -1 for a misplaced underscore.
func digitRun(s string) (string, int) {
	n, i := 0, 0
	for i < len(s) {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			n++
			i++
		case c == '_':
			if n == 0 || i+1 >= len(s) || s[i+1] < '0' || s[i+1] > '9' {
				return s, -1
			}
			i++
		default:
			return s[i:], n
		}
	}
	return "", n
}

// ParseInt is IntString as an int64: ok is false when s is not an int() literal or the value does not fit.
func ParseInt(s string) (int64, bool) {
	n, err := IntString(s)
	if err != nil || !n.IsInt64() {
		return 0, false
	}
	return n.Int64(), true
}
