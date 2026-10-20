package logx

import (
	"encoding/json"
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

func AddSecret(value string) {
	if value == "" {
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
	return slices.DeleteFunc(out, func(f string) bool { return f == "" })
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
