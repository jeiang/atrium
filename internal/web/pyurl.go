package web

import (
	"net/http"
	"net/url"
	"strings"
)

func sameOrigin(r *http.Request) bool {
	values := r.Header["Origin"]
	if len(values) == 0 {
		return true
	}
	origin := values[0]
	if origin == "null" {
		return false
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}
