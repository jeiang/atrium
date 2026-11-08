package web

import (
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/py"
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
	parts, err := py.URLSplit(origin)
	return err == nil && strings.EqualFold(parts.Netloc, r.Host)
}
