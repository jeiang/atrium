package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

type ID string
type Kind string
type State string

const (
	Unconfigured State = "unconfigured"
	Disabled     State = "disabled"
	Untested     State = "untested"
	OK           State = "ok"
	Failing      State = "failing"
	Expiring     State = "expiring"
	NeedsReauth  State = "needs_reauth"
	Away         State = "away"
)

const (
	UnreachableGrace = 6 * time.Hour
	graceLayout      = "2006-01-02 15:04 UTC"
	Screen           = "/settings/connections"
)

const (
	SourceOperator = "operator"
	SourceHost     = "host"
)

type Capability struct {
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config,omitempty"`
}

type CredentialInput struct {
	Slot        string
	Value       string
	HostFileRef string
	ExpiresAt   *time.Time
}

type Candidate struct {
	Label        string
	Config       json.RawMessage
	Capabilities []Capability
	Credentials  []CredentialInput
}

type Result struct {
	Capability string          `json:"capability"`
	State      State           `json:"state"`
	Reason     string          `json:"reason,omitempty"`
	ExpiresAt  *time.Time      `json:"expires_at,omitempty"`
	GraceUntil *time.Time      `json:"grace_until,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type Definition struct {
	Kind            Kind
	Offers          []string
	CredentialSlots []string
	Docs            string
	Shares          func(ID) (owner ID, slot string)
	Defaults        map[string]string
	Validate        func(Candidate) error
	Grace           time.Duration
	Interactive     []string
	Test            func(context.Context, *Lease, []Capability) ([]Result, error)
	// Silent replaces Test during a periodic health pass when the explicit
	// test has a visible side effect, such as a diagnostic push.
	Silent func(context.Context, *Lease, []Capability) ([]Result, error)
	Trace  func(ctx context.Context, lease *Lease, req TraceRequest) (Result, error)
}

type TraceRequest struct {
	Capability string
	Repeat     int
	Out        io.Writer
}

type Registry struct {
	mu       sync.RWMutex
	defs     map[Kind]Definition
	needs    []Need
	active   func(string) bool
	on       func(string) bool
	sync     *AccountSync
	notify   Notifier
	units    chan struct{}
	rechecks chan ID
}

func Compile(defs []Definition, needs []Need, sync *AccountSync, notify Notifier) (*Registry, error) {
	r := &Registry{defs: make(map[Kind]Definition, len(defs)), sync: sync, notify: notify, units: make(chan struct{}, 1), rechecks: make(chan ID, 16)}
	for _, d := range defs {
		if d.Kind == "" {
			return nil, errors.New("connections: a definition needs a kind")
		}
		if d.Test == nil {
			return nil, fmt.Errorf("connections: %s has no test action", d.Kind)
		}
		if _, dup := r.defs[d.Kind]; dup {
			return nil, fmt.Errorf("connections: duplicate kind %q", d.Kind)
		}
		if len(d.Offers) == 0 {
			return nil, fmt.Errorf("connections: %s offers no capability", d.Kind)
		}
		for i, c := range d.Offers {
			if c == "" || slices.Contains(d.Offers[:i], c) {
				return nil, fmt.Errorf("connections: %s offers an empty or repeated capability", d.Kind)
			}
		}
		r.defs[d.Kind] = d
	}
	for _, n := range needs {
		d, ok := r.defs[n.Kind]
		if !ok || n.Capability == "" || !slices.Contains(d.Offers, n.Capability) {
			return nil, &UnknownCapability{Module: n.Module, Kind: n.Kind, Capability: n.Capability}
		}
		if len(n.Features) == 0 || slices.Contains(n.Features, "") {
			return nil, fmt.Errorf("connections: %s needs %s from %s without naming the features it serves", n.Module, n.Capability, n.Kind)
		}
		r.needs = append(r.needs, n)
	}
	return r, nil
}

type UnknownCapability struct {
	Module     string
	Kind       Kind
	Capability string
}

func (e *UnknownCapability) Error() string {
	return fmt.Sprintf("connections: %s needs the capability %q from %s, which that kind does not offer", e.Module, e.Capability, e.Kind)
}

func (r *Registry) Lookup(kind Kind) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[kind]
	return d, ok
}

func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.defs))
	for k := range r.defs {
		out = append(out, k)
	}
	return out
}

type ErrNoRegistry struct{}

func (ErrNoRegistry) Error() string { return "connections: no registry in context" }

type registryKey struct{}

func With(ctx context.Context, r *Registry) context.Context {
	return context.WithValue(ctx, registryKey{}, r)
}

func From(ctx context.Context) (*Registry, error) {
	r, _ := ctx.Value(registryKey{}).(*Registry)
	if r == nil {
		return nil, ErrNoRegistry{}
	}
	return r, nil
}

var defaults sync.Map

type AccountSync struct {
	Accounts  config.HostAccountsFunc
	Rows      config.AccountRowsFunc
	Validate  func(context.Context, config.Host) error
	Reconcile func(context.Context, *sql.Tx, config.Host) error
	Reload    func(context.Context, *Store) error
}

func SetDefault(q db.Querier, s *Store) { defaults.Store(q, s) }

func DefaultFor(q db.Querier) *Store {
	if q == nil {
		return nil
	}
	if v, ok := defaults.Load(q); ok {
		return v.(*Store)
	}
	return nil
}

type CapabilityView struct {
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config"`
}

type CredentialView struct {
	Slot      string     `json:"slot"`
	Source    string     `json:"source"`
	Present   bool       `json:"present"`
	FileRef   string     `json:"file_ref,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	SetAt     *time.Time `json:"set_at,omitempty"`
	SetBy     string     `json:"set_by,omitempty"`
	Pinned    bool       `json:"pinned,omitempty"`
	Owner     ID         `json:"owner,omitempty"`
}

type View struct {
	ID           ID               `json:"id"`
	Kind         Kind             `json:"kind"`
	Label        string           `json:"label"`
	Enabled      bool             `json:"enabled"`
	ConfigSource string           `json:"config_source"`
	Config       json.RawMessage  `json:"config"`
	Capabilities []CapabilityView `json:"capabilities"`
	Credentials  []CredentialView `json:"credentials"`
	State        State            `json:"state"`
	Reason       string           `json:"reason"`
	CheckedAt    *time.Time       `json:"checked_at,omitempty"`
	LastOKAt     *time.Time       `json:"last_ok_at,omitempty"`
	ExpiresAt    *time.Time       `json:"expires_at,omitempty"`
	ReauthAt     *time.Time       `json:"reauth_at,omitempty"`
	Results      []Result         `json:"results"`
	GraceUntil   *time.Time       `json:"grace_until,omitempty"`
	Dependents   []Dependent      `json:"dependents"`
	Idle         string           `json:"idle,omitempty"`

	revision  int64
	transport transportOptions
}

func (v View) Configured() bool {
	for _, c := range v.Credentials {
		if c.Present {
			return true
		}
	}
	return len(v.Config) > 0 && string(v.Config) != "{}" && string(v.Config) != "null"
}

func (v View) Healthy() bool { return v.State == OK }

func (v View) Status() string {
	if v.GraceUntil != nil {
		return string(Away) + " until " + v.GraceUntil.UTC().Format(graceLayout)
	}
	return string(v.State)
}

func (v View) GraceLapsed() bool {
	if v.State != Failing || v.GraceUntil != nil || v.CheckedAt == nil {
		return false
	}
	for _, r := range v.Results {
		if r.State == Failing && r.GraceUntil != nil && v.CheckedAt.Before(*r.GraceUntil) {
			return true
		}
	}
	return false
}

func graceUntil(state State, results []Result, at time.Time) *time.Time {
	if state != Failing {
		return nil
	}
	var until *time.Time
	for _, r := range results {
		if r.State != Failing {
			continue
		}
		if r.GraceUntil == nil || !at.Before(*r.GraceUntil) {
			return nil
		}
		if until == nil || r.GraceUntil.Before(*until) {
			until = r.GraceUntil
		}
	}
	return until
}

func effective(state State, results []Result, at time.Time) State {
	if graceUntil(state, results, at) != nil {
		return Away
	}
	return state
}

func carryGrace(results, previous []Result, prior State, grace time.Duration, now time.Time) {
	for i := range results {
		if results[i].State != Failing {
			continue
		}
		results[i].GraceUntil = nil
		for _, p := range previous {
			if p.Capability == results[i].Capability && p.State == Failing && p.GraceUntil != nil {
				results[i].GraceUntil = p.GraceUntil
			}
		}
		if results[i].GraceUntil == nil && grace > 0 && (prior == OK || prior == Expiring) {
			until := now.Add(grace).UTC()
			results[i].GraceUntil = &until
		}
	}
}

func timeOr(t *time.Time, fallback time.Time) time.Time {
	if t == nil {
		return fallback
	}
	return *t
}

type UnknownID string

func (e UnknownID) Error() string { return "connections: unknown connection " + string(e) }

type UnknownKind string

func (e UnknownKind) Error() string { return "connections: unknown kind " + string(e) }

type Conflict struct {
	ID       ID
	Revision int64
}

func (e *Conflict) Error() string {
	return fmt.Sprintf("connections: %s changed since revision %d; reload and try again", e.ID, e.Revision)
}

type TestFailed struct {
	View View
}

func (e *TestFailed) Error() string {
	return fmt.Sprintf("test failed (%s): %s", e.View.State, e.View.Reason)
}

type CredentialEndpointChanged struct {
	ID    ID
	Field string
}

func (e *CredentialEndpointChanged) Error() string {
	return fmt.Sprintf("connections: %s has a different %s endpoint while it holds a stored credential; replace every retained credential in the same request, declare a credential file for it, or disconnect the stored credential first", e.ID, e.Field)
}

type EncryptedRequired struct{ Slot string }

func (e *EncryptedRequired) Error() string {
	return "connections: " + e.Slot + " can only be stored in an encrypted database; encrypt the database first"
}

type MissingCredential struct{ Slot string }

func (e MissingCredential) Error() string {
	return "connections: credential " + e.Slot + " is not set"
}

type PinnedCredential struct {
	ID   ID
	Slot string
}

func (e *PinnedCredential) Error() string {
	return "connections: " + string(e.ID) + " credential " + e.Slot + " is pinned by host configuration; change it there"
}

type NotConfigured struct{ ID ID }

func (e *NotConfigured) Error() string { return "connections: " + string(e.ID) + " is not configured" }

func aggregate(results []Result, enabled []Capability) (State, string) {
	if len(results) == 0 {
		return Untested, "no capability has been tested"
	}
	rank := map[State]int{NeedsReauth: 6, Unconfigured: 5, Failing: 4, Expiring: 3, Untested: 2, OK: 1}
	state, reason := OK, ""
	for _, r := range results {
		if rank[r.State] >= rank[state] {
			state, reason = r.State, r.Reason
		}
	}
	return state, reason
}

func unix(t *time.Time) *float64 {
	if t == nil {
		return nil
	}
	v := float64(t.UnixNano()) / 1e9
	return &v
}

func fromUnix(v *float64) *time.Time {
	if v == nil {
		return nil
	}
	t := time.Unix(0, int64(*v*1e9)).UTC()
	return &t
}

type Invalid struct{ Cause error }

func (e *Invalid) Error() string { return e.Cause.Error() }
func (e *Invalid) Unwrap() error { return e.Cause }

type CommittedError struct{ Cause error }

func (e *CommittedError) Error() string { return e.Cause.Error() }
func (e *CommittedError) Unwrap() error { return e.Cause }
