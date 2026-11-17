package pages

import (
	"strconv"
	"strings"
	"unicode"
)

func laxInt64(s string) (int64, bool) {
	body := strings.TrimSpace(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	whole, fraction, hasFraction := strings.Cut(body, ".")
	if hasFraction && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return 0, false
	}
	if whole == "" || strings.HasPrefix(whole, "_") || strings.HasSuffix(whole, "_") || strings.Contains(whole, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(whole, "_", ""), 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

func parseInt(s string) (int64, bool) {
	body := strings.TrimSpace(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	if body == "" || strings.HasPrefix(body, "_") || strings.HasSuffix(body, "_") || strings.Contains(body, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(body, "_", ""), 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}
