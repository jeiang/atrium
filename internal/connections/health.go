package connections

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

// Notifier delivers one operator notification for a health state transition.
type Notifier func(ctx context.Context, d *db.DB, title, body string) error

// announce reports a state change after its write committed. The first
// observation of a connection is evidence, not a transition, so it is silent.
func (s *Store) announce(ctx context.Context, id ID, previous, next State, reason string) {
	var n Notifier
	if s.reg != nil {
		n = s.reg.notify
	}
	if n == nil || s.d == nil || previous == next || previous == "" || previous == Untested || next == Untested {
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

// CheckAll tests every enabled connection that is configured. A connection
// whose definition has a silent check uses it, so a periodic pass never sends
// a visible diagnostic.
func (s *Store) CheckAll(ctx context.Context) error {
	return s.checkWhere(ctx, func(View) bool { return true })
}

// CheckStale re-verifies the connections that already carry health evidence.
// The first boot of a fresh database leaves untested connections for the
// periodic pass.
func (s *Store) CheckStale(ctx context.Context) error {
	return s.checkWhere(ctx, func(v View) bool { return v.CheckedAt != nil })
}

func (s *Store) checkWhere(ctx context.Context, keep func(View) bool) error {
	views, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, view := range views {
		if !view.Enabled || view.State == Unconfigured || view.State == Disabled || !keep(view) {
			continue
		}
		if _, err := s.check(ctx, view.ID); err != nil {
			return err
		}
	}
	return nil
}

// CheckOne re-verifies one connection, using its silent check when the
// definition has one.
func (s *Store) CheckOne(ctx context.Context, id ID) (View, error) {
	return s.check(ctx, id)
}

func (s *Store) check(ctx context.Context, id ID) (View, error) {
	lease, err := s.openLease(ctx, s.q, id, true)
	if err != nil {
		return View{}, err
	}
	defer lease.Close()
	def, ok := s.reg.Lookup(lease.kind)
	if !ok {
		return View{}, UnknownKind(lease.kind)
	}
	action := def.Test
	if def.Silent != nil {
		action = def.Silent
	}
	results, err := action(ctx, lease, lease.caps)
	if err != nil {
		return View{}, err
	}
	return s.record(ctx, id, lease.revision, results, false)
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

type failure struct {
	ID       ID
	Revision int64
}

var failures = make(chan failure, 64)

// Failed reports a runtime request that suggests the connection is unhealthy.
func Failed() <-chan failure { return failures }

func report(id ID, revision int64) {
	select {
	case failures <- failure{ID: id, Revision: revision}:
	default:
	}
}

type reporter struct {
	id       ID
	revision int64
	next     http.RoundTripper
}

func (r reporter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil && req.Context().Err() == nil || err == nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500) {
		report(r.id, r.revision)
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
