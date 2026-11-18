package davmirror

import (
	"errors"
	"net/http"
	"time"

	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/logx"
)

func retries(h connections.Hold) int {
	if h.Refusals > 0 {
		return 0
	}
	return 1
}

func throttled(err error) (*StatusError, bool) {
	status, ok := errors.AsType[*StatusError](err)
	if !ok {
		return nil, false
	}
	switch status.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return status, true
	}
	return nil, false
}

func settle(id connections.ID, result connections.Result, err error, prev connections.Hold, tag string, now time.Time) connections.Result {
	if err == nil {
		return result
	}
	h := prev
	if status, ok := throttled(err); ok {
		h = prev.Next(tag, status.RetryAfter, now)
		log.Warn("dav requests paused after a refusal", logx.Integration, string(id), logx.Capability, result.Capability, "status", status.Status, "step", status.Step, "refusals", h.Refusals, "until", h.Ends())
	}
	if h.Refusals == 0 {
		return result
	}
	h.Credential = tag
	return h.Attach(result, "iCloud", now)
}
