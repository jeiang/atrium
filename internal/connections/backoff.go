package connections

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/runtime"
)

type Hold struct {
	Refusals   int       `json:"refusals"`
	Until      time.Time `json:"backoff_until"`
	Credential string    `json:"credential"`
}

var holdSteps = []time.Duration{time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour}

func CredentialTag(secret string) string {
	sum := sha256.Sum256([]byte("atrium dav backoff\x00" + secret))
	return hex.EncodeToString(sum[:4])
}

func HeldBy(stored Result, tag string) Hold {
	var h Hold
	if stored.State == OK || stored.State == Expiring || len(stored.Data) == 0 {
		return Hold{}
	}
	if err := json.Unmarshal(stored.Data, &h); err != nil || h.Credential != tag {
		return Hold{}
	}
	return h
}

func (h Hold) Active(now time.Time) bool { return h.Refusals > 0 && now.Before(h.Until) }

func (h Hold) Ends() string { return h.Until.UTC().Format("2006-01-02 15:04 UTC") }

func (h Hold) Next(tag string, retryAfter time.Duration, now time.Time) Hold {
	h.Credential = tag
	h.Refusals++
	wait := max(holdSteps[min(h.Refusals, len(holdSteps))-1], retryAfter)
	h.Until = now.Add(wait).UTC().Truncate(time.Second)
	return h
}

const holdWords = "; atrium sends "

func (h Hold) Attach(result Result, service string, now time.Time) Result {
	result.Data, _ = json.Marshal(h)
	if i := strings.Index(result.Reason, holdWords); i >= 0 {
		result.Reason = result.Reason[:i]
	}
	if h.Active(now) {
		result.Reason += holdWords + service + " no request for this until " + h.Ends() + ", backing off after " + Plural(h.Refusals, "refusal") + " in a row"
	}
	return result
}

func (h Hold) Skipped(id ID, capability string) error {
	return &Unmet{Connection: id, Capability: capability, Until: h.Until,
		Reason: fmt.Sprintf("skipped, backing off until %s after %s; no request was sent", h.Ends(), Plural(h.Refusals, "refusal"))}
}

type RemoteRefusal struct {
	Status     int
	RetryAfter time.Duration
	Until      time.Time
	Err        error
}

func (e *RemoteRefusal) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "the service answered " + strconv.Itoa(e.Status)
}

func (e *RemoteRefusal) Unwrap() error { return e.Err }

func HeldUntil(err error) (time.Time, bool) {
	if r, ok := errors.AsType[*RemoteRefusal](err); ok && !r.Until.IsZero() {
		return r.Until, true
	}
	if u, ok := errors.AsType[*Unmet](err); ok && !u.Until.IsZero() {
		return u.Until, true
	}
	return time.Time{}, false
}

func Refuses(status int, retryAfter time.Duration) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	case http.StatusServiceUnavailable:
		return retryAfter > 0
	}
	return false
}

func RetryAfter(value string, now time.Time) time.Duration {
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

func Plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

type holdKey struct {
	reg        *Registry
	id         ID
	capability string
}

var holds sync.Map

func (l *Lease) credentialTag() string {
	values := make([]string, 0, len(l.creds))
	for _, slot := range slices.Sorted(maps.Keys(l.creds)) {
		v, _ := l.Credential(slot)
		values = append(values, v)
	}
	return CredentialTag(strings.Join(values, "\x00"))
}

func (l *Lease) holdKey(capability string) holdKey {
	return holdKey{reg: l.store.reg, id: l.id, capability: capability}
}

func (l *Lease) hold(ctx context.Context, capability string) (Hold, string) {
	tag := l.credentialTag()
	if v, ok := holds.Load(l.holdKey(capability)); ok {
		if h := v.(Hold); h.Refusals == 0 || h.Credential == tag {
			return h, tag
		}
	}
	var h Hold
	if stored, ok := l.Stored(ctx, capability); ok {
		h = HeldBy(stored, tag)
	}
	holds.Store(l.holdKey(capability), h)
	return h, tag
}

func (l *Lease) Held(ctx context.Context, capability string) (Hold, bool) {
	if l == nil || l.store == nil || capability == "" {
		return Hold{}, false
	}
	h, _ := l.hold(ctx, capability)
	return h, h.Active(time.Now())
}

func (l *Lease) Refuse(ctx context.Context, capability string, err error) error {
	r, ok := errors.AsType[*RemoteRefusal](err)
	if !ok {
		r = &RemoteRefusal{Err: err}
	}
	out := *r
	out.Until = l.refused(ctx, capability, r.Status, r.RetryAfter, r.Error()).Until
	return &out
}

func (l *Lease) refused(ctx context.Context, capability string, status int, retryAfter time.Duration, reason string) Hold {
	now := time.Now()
	prev, tag := l.hold(ctx, capability)
	if prev.Active(now) {
		return prev
	}
	h := prev.Next(tag, retryAfter, now)
	holds.Store(l.holdKey(capability), h)
	log.Warn("requests paused after a refusal", logx.Integration, string(l.id), "capability", capability, "status", status, "refusals", h.Refusals, "until", h.Ends())
	report(l.store.reg, outcome{ID: l.id, Revision: l.revision, Capability: capability, Held: &h, Reason: reason})
	return h
}

func succeededHold(reg *Registry, id ID, capability string) {
	holds.Store(holdKey{reg: reg, id: id, capability: capability}, Hold{})
}

func (l *Lease) skipUnattended(ctx context.Context, capability string) error {
	if !runtime.IsUnattended(ctx) {
		return nil
	}
	if h, held := l.Held(ctx, capability); held {
		return h.Skipped(l.id, capability)
	}
	return nil
}

func (s *Store) RecordHold(ctx context.Context, o outcome) error {
	if o.Held == nil {
		return nil
	}
	v, err := s.Get(ctx, o.ID)
	if err != nil || v.State == Unconfigured || v.State == Disabled {
		return err
	}
	result := Result{Capability: o.Capability, State: Failing, Reason: o.Reason}
	for _, r := range v.Results {
		if r.Capability == o.Capability && unhealthy(r.State) {
			result = r
		}
	}
	label := v.Label
	if label == "" {
		label = string(v.ID)
	}
	result = o.Held.Attach(result, label, time.Now())
	if err := s.observe(ctx, o.ID, o.Revision, result, false); err != nil && !errors.As(err, new(*Conflict)) {
		return err
	}
	return nil
}

func carryHolds(results, prior []Result) {
	for i := range results {
		if len(results[i].Data) > 0 || !unhealthy(results[i].State) {
			continue
		}
		for _, p := range prior {
			var h Hold
			if p.Capability == results[i].Capability && json.Unmarshal(p.Data, &h) == nil && h.Refusals > 0 {
				results[i].Data = p.Data
			}
		}
	}
}
