package connections

import (
	"context"
	"database/sql"
	"encoding/json"
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
	if n == nil || s.d == nil || previous == next && !unhealthy(next) || next == Untested || next == Away {
		return
	}
	if (previous == "" || previous == Untested) && next != Failing && next != NeedsReauth && next != Disabled {
		return
	}
	if previous == Away && (next == OK || next == Expiring) {
		return
	}
	label := string(id)
	hint := ""
	if view, err := s.Get(context.WithoutCancel(ctx), id); err == nil {
		if view.Idle != "" {
			return
		}
		if view.Label != "" {
			label = view.Label
		}
		if next == Unconfigured && view.LastOKAt == nil {
			return
		}
		if next == NeedsReauth {
			hint = RepairHint(view)
		}
	}
	if !s.damp(id, previous, next) {
		return
	}
	state := strings.ReplaceAll(string(next), "_", " ")
	body := label + ": " + state
	if reason != "" {
		body += ": " + reason
	}
	if hint != "" {
		body += "; " + hint
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

const PinnedHint = "the credential is changed in host configuration"

func RepairHint(v View) string {
	for _, c := range v.Credentials {
		switch {
		case c.Owner != "" && c.Pinned:
			return "it uses the credential of " + string(c.Owner) + "; " + PinnedHint
		case c.Owner != "":
			return "it uses the credential of " + string(c.Owner) + "; change it there"
		case c.Pinned:
			return PinnedHint
		}
	}
	return ""
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
		return s.record(ctx, id, lease.revision, 0, nil, []Result{{State: Failing, Reason: err.Error()}}, commits...)
	}
	action := def.Test
	if def.Silent != nil {
		action = def.Silent
	}
	results, err := action(ctx, lease, lease.caps)
	if err != nil {
		return View{}, err
	}
	return s.record(ctx, id, lease.revision, def.Grace, def.Interactive, results, commits...)
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
	// Capability is the capability the request used, when the kind says
	// which one its endpoint serves.
	Capability string
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

type succeededKey struct {
	reg        *Registry
	id         ID
	capability string
}

var (
	outcomes  sync.Map
	succeeded sync.Map
	observed  sync.Map
	reporters sync.Map
)

// Outcomes receives the request outcomes of leases opened under reg, the
// connection registry of one running app. Each serving app registers its own
// channel, so outcomes never cross to another app in the same process.
func Outcomes(reg *Registry) (<-chan outcome, func()) {
	ch := make(chan outcome, 64)
	outcomes.Store(reg, ch)
	return ch, func() { outcomes.CompareAndDelete(reg, ch) }
}

func report(reg *Registry, o outcome) {
	ch, registered := outcomes.Load(reg)
	if !registered {
		return
	}
	key := succeededKey{reg: reg, id: o.ID, capability: o.Capability}
	if o.OK {
		now := time.Now().UnixNano()
		if last, seen := succeeded.Load(key); seen && now-last.(int64) < int64(time.Minute) {
			return
		}
		succeeded.Store(key, now)
	} else {
		// The first success after a failure is the one that recovers.
		succeeded.Delete(key)
	}
	select {
	case ch.(chan outcome) <- o:
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
			l.reported(r.State)
			return
		}
	}
	select {
	case ch.(chan RuntimeReport) <- RuntimeReport{ID: l.id, revision: l.revision, store: l.store, result: r}:
		observed.Store(key, observation{state: r.State, at: now})
		l.reported(r.State)
	default:
	}
}

func (l *Lease) Stored(ctx context.Context, capability string) (Result, bool) {
	if l.store == nil || l.store.q == nil {
		return Result{}, false
	}
	var raw string
	if err := l.store.q.QueryRowContext(ctx, "SELECT results_json FROM connections WHERE id = ?", l.id).Scan(&raw); err != nil {
		return Result{}, false
	}
	var results []Result
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return Result{}, false
	}
	for _, r := range results {
		if r.Capability == capability {
			return r, true
		}
	}
	return Result{}, false
}

func (l *Lease) Settle(ctx context.Context, r Result) error {
	if l.store == nil || l.store.d == nil || r.Capability == "" {
		return nil
	}
	if _, ok := reporters.Load(l.store.d); !ok {
		return nil
	}
	current, err := l.store.Get(ctx, l.id)
	if err != nil || current.State == Unconfigured || current.State == Disabled {
		return err
	}
	if err := l.store.Observe(ctx, l.id, l.revision, r); err != nil && !errors.As(err, new(*Conflict)) {
		return err
	}
	observed.Store(observedKey{store: l.store, id: l.id, capability: r.Capability}, observation{state: r.State, at: time.Now().UnixNano()})
	l.reported(r.State)
	return nil
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
	reg        *Registry
	id         ID
	revision   int64
	capability string
	next       http.RoundTripper
}

func (r reporter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	switch {
	case err != nil && req.Context().Err() == nil || err == nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500):
		report(r.reg, outcome{ID: r.id, Revision: r.revision, Capability: r.capability})
	case err == nil && resp.StatusCode < http.StatusBadRequest:
		report(r.reg, outcome{ID: r.id, Revision: r.revision, Capability: r.capability, OK: true})
	}
	return resp, err
}

func (r reporter) Unwrap() http.RoundTripper { return r.next }

func (r reporter) CloseIdleConnections() {
	if c, ok := r.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (l *Lease) reporting(endpoint string, client *http.Client) *http.Client {
	if client.Transport == nil || l.store == nil || l.store.reg == nil {
		return client
	}
	client.Transport = reporter{reg: l.store.reg, id: l.id, revision: l.revision, capability: l.serves(endpoint), next: client.Transport}
	return client
}

// serves names the capability that requests to endpoint use, or "" when the
// kind does not say and offers more than one.
func (l *Lease) serves(endpoint string) string {
	def, ok := l.store.reg.Lookup(l.kind)
	switch {
	case !ok:
		return ""
	case def.Serves != nil:
		return def.Serves[endpoint]
	case len(def.Offers) == 1:
		return def.Offers[0]
	}
	return ""
}

func (l *Lease) reported(state State) {
	if l.cause != nil && unhealthy(state) {
		l.cause()
	}
}

const (
	flapWindow   = 6 * time.Hour
	lastingAfter = 30 * time.Minute
)

type noticeKey struct {
	d  *db.DB
	id ID
}

type noticeMemory struct {
	toldAt time.Time
	open   bool
	heldAt time.Time
}

var (
	noticesMu sync.Mutex
	notices   = map[noticeKey]noticeMemory{}
)

func unhealthy(s State) bool { return s == Failing || s == NeedsReauth }

func (s *Store) damp(id ID, previous, next State) bool {
	key := noticeKey{d: s.d, id: id}
	noticesMu.Lock()
	defer noticesMu.Unlock()
	m, seen := notices[key]
	if !seen {
		m = noticeMemory{open: unhealthy(previous)}
	}
	send, m := dampNotice(m, previous, next, s.now())
	if unhealthy(next) || next == OK || next == Expiring {
		notices[key] = m
	} else {
		delete(notices, key)
	}
	return send
}

func dampNotice(m noticeMemory, previous, next State, now time.Time) (bool, noticeMemory) {
	switch {
	case unhealthy(next) && previous == next:
		if m.open || m.heldAt.IsZero() || now.Sub(m.heldAt) < lastingAfter {
			return false, m
		}
		return true, noticeMemory{toldAt: now, open: true}
	case unhealthy(next):
		if !m.toldAt.IsZero() && now.Sub(m.toldAt) < flapWindow {
			if !m.open && m.heldAt.IsZero() {
				m.heldAt = now
			}
			return false, m
		}
		return true, noticeMemory{toldAt: now, open: true}
	case unhealthy(previous) && (next == OK || next == Expiring):
		m.heldAt = time.Time{}
		if !m.open {
			return false, m
		}
		m.open = false
		return true, m
	}
	return true, m
}
