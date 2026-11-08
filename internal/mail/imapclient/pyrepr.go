package imapclient

import (
	"strconv"
	"strings"
)

func (d datum) repr() string {
	switch {
	case d.none:
		return "none"
	case d.tuple:
		return "(" + strconv.Quote(string(d.text)) + ", " + strconv.Quote(string(d.lit)) + ")"
	}
	return strconv.Quote(string(d.text))
}

func listRepr(data []datum) string {
	parts := make([]string, len(data))
	for i, d := range data {
		parts[i] = d.repr()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func (d datum) str() string {
	if d.none || d.tuple {
		return d.repr()
	}
	return latin1(d.text)
}

func split(s string) []string {
	if out := strings.Fields(s); out != nil {
		return out
	}
	return []string{}
}
