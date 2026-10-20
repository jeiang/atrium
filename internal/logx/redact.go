package logx

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
)

var redactor struct {
	mu       sync.RWMutex
	known    map[string]struct{}
	replacer *strings.Replacer
}

const minSecret = 8

func AddSecret(value string) {
	if len(value) < minSecret {
		return
	}
	redactor.mu.Lock()
	defer redactor.mu.Unlock()
	if redactor.known == nil {
		redactor.known = map[string]struct{}{}
	}
	changed := false
	for _, form := range forms(value) {
		if _, ok := redactor.known[form]; !ok {
			redactor.known[form] = struct{}{}
			changed = true
		}
	}
	if !changed {
		return
	}
	all := make([]string, 0, len(redactor.known))
	for form := range redactor.known {
		all = append(all, form)
	}
	slices.SortFunc(all, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	pairs := make([]string, 0, 2*len(all))
	for _, form := range all {
		pairs = append(pairs, form, Redacted)
	}
	redactor.replacer = strings.NewReplacer(pairs...)
}

func forms(value string) []string {
	out := []string{value, url.QueryEscape(value), url.PathEscape(value)}
	if raw, err := json.Marshal(value); err == nil {
		out = append(out, string(raw[1:len(raw)-1]))
	}
	if ascii := asciiEscaped(value); ascii != value {
		out = append(out, ascii)
	}
	return slices.DeleteFunc(out, func(f string) bool { return f == "" })
}

func asciiEscaped(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			fmt.Fprintf(&b, "\\u%04x", r)
		case r < 0x80:
			b.WriteRune(r)
		case r >= 0x10000:
			r -= 0x10000
			fmt.Fprintf(&b, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			fmt.Fprintf(&b, "\\u%04x", r)
		}
	}
	return b.String()
}

func active() bool {
	redactor.mu.RLock()
	defer redactor.mu.RUnlock()
	return redactor.replacer != nil
}

func Scrub(s string) string {
	redactor.mu.RLock()
	r := redactor.replacer
	redactor.mu.RUnlock()
	if r == nil {
		return s
	}
	return r.Replace(s)
}
