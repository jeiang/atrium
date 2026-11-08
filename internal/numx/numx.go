package numx

import (
	"strconv"
	"strings"
)

// ParseInt parses a decimal integer, returning ok=false on failure.
func ParseInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

// ParseFloat parses a float, returning the strconv error on failure.
func ParseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// LaxInt64 parses an integer from a JSON-ish value.
func LaxInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n == float64(int64(n))
	case string:
		return ParseInt(n)
	}
	return 0, false
}

// Sum adds values.
func Sum(values []float64) float64 {
	var total float64
	for _, v := range values {
		total += v
	}
	return total
}

// FloatRepr formats f like Python's repr.
func FloatRepr(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
