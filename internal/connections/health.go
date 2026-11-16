package connections

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

// Notifier delivers one operator notification for a health state transition.
type Notifier func(ctx context.Context, d *db.DB, title, body string) error

// announce reports a state change after its write committed. The first
// observation of a connection notifies only when it is a failure.
func (s *Store) announce(ctx context.Context, id ID, previous, next State, reason string) {
	var n Notifier
	if s.reg != nil {
		n = s.reg.notify
	}
	if n == nil || s.d == nil || previous == next || next == Untested || next == Away {
		return
	}
	if (previous == "" || previous == Untested) && next != Failing && next != NeedsReauth && next != Disabled {
		return
	}
	if previous == Away && (next == OK || next == Expiring) {
		return
	}
	label := string(id)
	if view, err := s.Get(context.WithoutCancel(ctx), id); err == nil && view.Label != "" {
		label = view.Label
	}
	state := strings.ReplaceAll(string(next), "_", " ")
	body := label + ": " + state
	if reason != "" {
		body += ": " + reason
	}
	if err := n(context.WithoutCancel(ctx), s.d, "atrium: "+label+" "+state, body); err != nil {
		log.Warn("connection notification failed", logx.Integration, string(id), logx.Err, err.Error())
	}
}

// CheckOne re-verifies one connection, using its silent check when the
// definition has one.
func (s *Store) CheckOne(ctx context.Context, id ID, commits ...Commit) (View, error) {
	return s.check(ctx, id, commits...)
}

func (s *Store) check(ctx context.Context, id ID, commits ...Commit) (View, error) {
	lease, err := s.openLease(ctx, s.q, id, true)
	if err != nil {
		return View{}, err
	}
	defer lease.Close()
	def, ok := s.reg.Lookup(lease.kind)
	if !ok {
		return View{}, UnknownKind(lease.kind)
	}
	if err := validateEndpointURLs(id, lease.Config()); err != nil {
		var invalid *Invalid
		if !errors.As(err, &invalid) {
			return View{}, err
		}
		return s.record(ctx, id, lease.revision, 0, []Result{{State: Failing, Reason: err.Error()}}, commits...)
	}
	action := def.Test
	if def.Silent != nil {
		action = def.Silent
	}
	results, err := action(ctx, lease, lease.caps)
	if err != nil {
		return View{}, err
	}
	return s.record(ctx, id, lease.revision, def.Grace, results, commits...)
}

type HealthState struct {
	ID    string
	State State
}

func HealthStates(ctx context.Context, q db.Querier) ([]HealthState, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, state FROM connections ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (HealthState, error) {
		var h HealthState
		err := r.Scan(&h.ID, &h.State)
		return h, err
	})
}

type outcome struct {
	ID       ID
	Revision int64
	OK       bool
	store    *Store
	result   *Result
}

type observedKey struct {
	store      *Store
	id         ID
	capability string
}

type observation struct {
	state State
	at    int64
}

var (
	outcomes  = make(chan outcome, 64)
	succeeded sync.Map
	observed  sync.Map
)

// Outcomes reports runtime requests that suggest the connection's health
// changed: failures, and at most one success per connection a minute.
func Outcomes() <-chan outcome { return outcomes }

func report(id ID, revision int64, ok bool) {
	if ok {
		now := time.Now().UnixNano()
		if last, seen := succeeded.Load(id); seen && now-last.(int64) < int64(time.Minute) {
			return
		}
		succeeded.Store(id, now)
	}
	select {
	case outcomes <- outcome{ID: id, Revision: revision, OK: ok}:
	default:
	}
}

func (l *Lease) Report(r Result) {
	if l.store == nil || l.store.d == nil || r.Capability == "" {
		return
	}
	key := observedKey{store: l.store, id: l.id, capability: r.Capability}
	now := time.Now().UnixNano()
	if last, seen := observed.Load(key); seen {
		if prev := last.(observation); prev.state == r.State && now-prev.at < int64(time.Minute) {
			return
		}
	}
	select {
	case outcomes <- outcome{ID: l.id, Revision: l.revision, OK: r.State == OK, store: l.store, result: &r}:
		observed.Store(key, observation{state: r.State, at: now})
	default:
	}
}

func (o outcome) Record(ctx context.Context) (bool, error) {
	if o.result == nil {
		return false, nil
	}
	current, err := o.store.Get(ctx, o.ID)
	if err != nil || !changes(current, *o.result) {
		return true, err
	}
	if err := o.store.Observe(ctx, o.ID, o.Revision, *o.result); err != nil && !errors.As(err, new(*Conflict)) {
		return true, err
	}
	return true, nil
}

func changes(current View, r Result) bool {
	if current.State == Unconfigured || current.State == Disabled {
		return false
	}
	for _, p := range current.Results {
		if p.Capability == r.Capability {
			return !same(p.State, r.State)
		}
	}
	return r.State != OK
}

func same(a, b State) bool {
	healthy := func(s State) bool { return s == OK || s == Expiring }
	failed := func(s State) bool { return s == Failing || s == NeedsReauth }
	return a == b || healthy(a) && healthy(b) || failed(a) && failed(b)
}

type reporter struct {
	id       ID
	revision int64
	next     http.RoundTripper
}

func (r reporter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	switch {
	case err != nil && req.Context().Err() == nil || err == nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500):
		report(r.id, r.revision, false)
	case err == nil && resp.StatusCode < http.StatusBadRequest:
		report(r.id, r.revision, true)
	}
	return resp, err
}

func (r reporter) Unwrap() http.RoundTripper { return r.next }

func (r reporter) CloseIdleConnections() {
	if c, ok := r.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (l *Lease) reporting(client *http.Client) *http.Client {
	if client.Transport == nil {
		return client
	}
	client.Transport = reporter{id: l.id, revision: l.revision, next: client.Transport}
	return client
}
