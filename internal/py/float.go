package py

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

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

func FormatPercent(x float64, prec int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return FormatFixed(x, prec) + "%"
	}
	return FormatFixed(float64(x*100), prec) + "%"
}

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

func ParseInt(s string) (int64, bool) {
	n, err := IntString(s)
	if err != nil || !n.IsInt64() {
		return 0, false
	}
	return n.Int64(), true
}

func FloorDiv(a, b float64) float64 {
	mod := math.Mod(a, b)
	div := (a - mod) / b
	if mod != 0 && (b < 0) != (mod < 0) {
		div--
	}
	if div == 0 {
		return math.Copysign(0, a/b)
	}
	floored := math.Floor(div)
	if div-floored > 0.5 {
		floored++
	}
	return floored
}
