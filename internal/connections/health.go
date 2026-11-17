package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

// Notifier delivers one operator notification for a health state transition.
type Notifier func(ctx context.Context, d *db.DB, title, body, occurrence string) error

func (s *Store) AnnounceRemoved(ctx context.Context) error {
	if s.d == nil {
		return nil
	}
	type removed struct {
		id       ID
		revision int64
	}
	rows, err := s.d.Read.QueryContext(ctx,
		"SELECT id, revision FROM connections WHERE config_source = 'host' AND enabled = 0 AND state = 'disabled' AND reason = ? ORDER BY id", reasonRemovedFromHost)
	if err != nil {
		return err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (removed, error) {
		var row removed
		return row, r.Scan(&row.id, &row.revision)
	})
	if err != nil {
		return err
	}
	for _, row := range all {
		s.notice(ctx, row.id, "", Disabled, reasonRemovedFromHost, fmt.Sprintf("connection-removed:%s:%d", row.id, row.revision))
	}
	return nil
}

func (s *Store) announce(ctx context.Context, id ID, previous, next State, reason string) {
	s.notice(ctx, id, previous, next, reason, "")
}

func (s *Store) notice(ctx context.Context, id ID, previous, next State, reason, occurrence string) {
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
	if view, err := s.Get(context.WithoutCancel(ctx), id); err == nil {
		if view.Idle != "" {
			return
		}
		if view.Label != "" {
			label = view.Label
		}
	}
	state := strings.ReplaceAll(string(next), "_", " ")
	body := label + ": " + state
	if reason != "" {
		body += ": " + reason
	}
	if RepairClass(next) != "" {
		if features := s.affectedFeatures(context.WithoutCancel(ctx), id); len(features) > 0 {
			body += "; affects " + strings.Join(features, ", ")
		}
	}
	if err := n(context.WithoutCancel(ctx), s.d, "atrium: "+label+" "+state, body, occurrence); err != nil {
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
}

type RuntimeReport struct {
	ID       ID
	revision int64
	store    *Store
	result   Result
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
	reporters sync.Map
)

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

func Reports(d *db.DB) (<-chan RuntimeReport, func()) {
	ch := make(chan RuntimeReport, 64)
	reporters.Store(d, ch)
	return ch, func() { reporters.CompareAndDelete(d, ch) }
}

func (l *Lease) Report(r Result) {
	if l.store == nil || l.store.d == nil || r.Capability == "" {
		return
	}
	ch, ok := reporters.Load(l.store.d)
	if !ok {
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
	case ch.(chan RuntimeReport) <- RuntimeReport{ID: l.id, revision: l.revision, store: l.store, result: r}:
		observed.Store(key, observation{state: r.State, at: now})
	default:
	}
}

func (r RuntimeReport) Record(ctx context.Context) error {
	current, err := r.store.Get(ctx, r.ID)
	if err != nil || !changes(current, r.result) {
		return err
	}
	if err := r.store.Observe(ctx, r.ID, r.revision, r.result); err != nil && !errors.As(err, new(*Conflict)) {
		return err
	}
	return nil
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
