package davmirror

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/connections"
)

type hold struct {
	Refusals   int       `json:"refusals"`
	Until      time.Time `json:"backoff_until"`
	Credential string    `json:"credential"`
}

var holdSteps = []time.Duration{time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour}

func credentialTag(password string) string {
	sum := sha256.Sum256([]byte("atrium dav backoff\x00" + password))
	return hex.EncodeToString(sum[:4])
}

func heldBy(stored connections.Result, tag string) hold {
	var h hold
	if stored.State == connections.OK || stored.State == connections.Expiring || len(stored.Data) == 0 {
		return hold{}
	}
	if err := json.Unmarshal(stored.Data, &h); err != nil || h.Credential != tag {
		return hold{}
	}
	return h
}

func (h hold) active(now time.Time) bool { return h.Refusals > 0 && now.Before(h.Until) }

func (h hold) retries() int {
	if h.Refusals > 0 {
		return 0
	}
	return 1
}

func (h hold) until() string { return h.Until.UTC().Format("2006-01-02 15:04 UTC") }

func (h hold) words() string {
	return "atrium sends iCloud no request for this until " + h.until() + ", backing off after " + plural(h.Refusals, "refusal") + " in a row"
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

func settle(result connections.Result, err error, prev hold, tag string, now time.Time) connections.Result {
	if err == nil {
		return result
	}
	h := prev
	h.Credential = tag
	if status, ok := throttled(err); ok {
		h.Refusals++
		wait := max(holdSteps[min(h.Refusals, len(holdSteps))-1], status.RetryAfter)
		h.Until = now.Add(wait).UTC().Truncate(time.Second)
		log.Warn("dav requests paused after a refusal", "status", status.Status, "step", status.Step, "refusals", h.Refusals, "until", h.until())
	}
	if h.Refusals == 0 {
		return result
	}
	result.Data, _ = json.Marshal(h)
	if h.active(now) {
		result.Reason += "; " + h.words()
	}
	return result
}

func skipped(lease *connections.Lease, capability string, h hold) error {
	return &connections.Unmet{Connection: lease.ID(), Capability: capability,
		Reason: fmt.Sprintf("skipped, backing off until %s after %s; no request was sent", h.until(), plural(h.Refusals, "refusal"))}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
