package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"time"

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
	Data       json.RawMessage `json:"data,omitempty"`
}

type Definition struct {
	Kind     Kind
	Validate func(Candidate) error
	Test     func(context.Context, *Lease, []Capability) ([]Result, error)
}

type Registry struct {
	mu   sync.RWMutex
	defs map[Kind]Definition
}

func NewRegistry() *Registry { return &Registry{defs: map[Kind]Definition{}} }

func (r *Registry) Register(d Definition) error {
	if d.Kind == "" {
		return errors.New("connections: a definition needs a kind")
	}
	if d.Test == nil {
		return fmt.Errorf("connections: %s has no test action", d.Kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.defs[d.Kind]; dup {
		return fmt.Errorf("connections: duplicate kind %q", d.Kind)
	}
	r.defs[d.Kind] = d
	return nil
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

var defaults sync.Map

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

var defaultRegistry = NewRegistry()

func Register(d Definition) {
	if err := defaultRegistry.Register(d); err != nil {
		panic(err)
	}
}

func DefaultRegistry() *Registry { return defaultRegistry }

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

type EncryptedRequired struct{ Slot string }

func (e *EncryptedRequired) Error() string {
	return "connections: " + e.Slot + " can only be stored in an encrypted database; encrypt the database first"
}

type MissingCredential struct{ Slot string }

func (e MissingCredential) Error() string {
	return "connections: credential " + e.Slot + " is not set"
}

type NotConfigured struct{ ID ID }

func (e *NotConfigured) Error() string { return "connections: " + string(e.ID) + " is not configured" }

func aggregate(results []Result, enabled []Capability) (State, string) {
	if len(results) == 0 {
		return Untested, "no capability has been tested"
	}
	rank := map[State]int{NeedsReauth: 5, Failing: 4, Expiring: 3, Untested: 2, OK: 1}
	state, reason := OK, ""
	for _, r := range results {
		if rank[r.State] > rank[state] {
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
