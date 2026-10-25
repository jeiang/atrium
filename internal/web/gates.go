package web

import "net/http"

type adminRule struct {
	method  string
	pattern string
}

var adminRules = []adminRule{
	{"", "/settings"},
	{"", "/settings/*"},
	{"", "/secrets/*"},
	{"", "/integrations"},
	{"", "/integrations/*"},
	{"", "/modules"},
	{"", "/modules/*"},
	{"", "/budget"},
	{"", "/budget/*"},
	{http.MethodPost, "/live/mode"},
	{http.MethodPost, "/live/go-live"},
}

func adminRoute(r *http.Request) bool {
	paths := []string{r.URL.Path}
	if r.URL.RawPath != "" {
		paths = append(paths, r.URL.RawPath)
	}
	for _, rule := range adminRules {
		if rule.method != "" && rule.method != r.Method {
			continue
		}
		for _, p := range paths {
			if routeMatches(rule.pattern, p) {
				return true
			}
		}
	}
	return false
}
