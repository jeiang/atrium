package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

type Need struct {
	Module     string
	Kind       Kind
	Capability string
	Soft       bool
	Features   []string
}

const (
	RepairAuth        = "auth"
	RepairUnreachable = "unreachable"
	RepairConfig      = "config"

	SeverityBlocker  = "blocker"
	SeverityDegraded = "degraded"
	SeverityNotice   = "notice"

	FeatureAvailable   = "available"
	FeatureDegraded    = "degraded"
	FeatureUnavailable = "unavailable"
)

type Binding struct {
	Module          string   `json:"module"`
	Active          bool     `json:"active"`
	Kind            Kind     `json:"kind"`
	Capability      string   `json:"capability"`
	Soft            bool     `json:"soft"`
	Features        []string `json:"features"`
	Candidates      []ID     `json:"candidates"`
	Connection      ID       `json:"connection,omitempty"`
	ConnectionLabel string   `json:"connection_label,omitempty"`
	State           State    `json:"state"`
	Reason          string   `json:"reason,omitempty"`
	Met             bool     `json:"met"`
	Availability    string   `json:"availability"`
	Repair          string   `json:"repair,omitempty"`
}

type Repair struct {
	ID         string   `json:"id"`
	Connection ID       `json:"connection"`
	Label      string   `json:"label"`
	Class      string   `json:"class"`
	State      State    `json:"state"`
	Reason     string   `json:"reason"`
	Owner      string   `json:"owner"`
	Severity   string   `json:"severity"`
	Features   []string `json:"features"`
	Fix        string   `json:"fix"`
	Hint       string   `json:"hint,omitempty"`
}

type Dependent struct {
	Module     string   `json:"module"`
	Active     bool     `json:"active"`
	Capability string   `json:"capability"`
	Soft       bool     `json:"soft"`
	Features   []string `json:"features"`
}

type Dependencies struct {
	Needs   []Binding `json:"needs"`
	Repairs []Repair  `json:"repairs"`
}

func RepairID(id ID, class string) string { return "connection:" + string(id) + ":" + class }

func RepairClass(s State) string {
	switch s {
	case NeedsReauth:
		return RepairAuth
	case Failing:
		return RepairUnreachable
	case Unconfigured:
		return RepairConfig
	}
	return ""
}

func Meets(s State) bool {
	return s == OK || s == Expiring || s == Away || s == Untested
}

func Serves(v View, offers []string, capability string) bool {
	if !slices.Contains(offers, capability) {
		return false
	}
	listed := false
	for _, c := range v.Capabilities {
		if !slices.Contains(offers, c.Name) {
			continue
		}
		if c.Name == capability {
			return c.Enabled
		}
		listed = listed || c.Enabled
	}
	return !listed
}

func CapabilityState(v View, capability string) (State, string) {
	if !v.Enabled {
		return Disabled, v.Reason
	}
	if v.GraceUntil != nil {
		return Away, v.Reason
	}
	switch v.State {
	case Unconfigured, Disabled, Untested:
		return v.State, v.Reason
	}
	for _, r := range v.Results {
		if r.Capability == capability {
			return r.State, r.Reason
		}
	}
	return v.State, v.Reason
}

func (r *Registry) Offers(kind Kind) []string {
	d, _ := r.Lookup(kind)
	return slices.Clone(d.Offers)
}

func (r *Registry) Needs() []Need {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.needs)
}

func (r *Registry) Activate(active, on func(module string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active, r.on = active, on
}

func (r *Registry) isActive(module string) bool {
	r.mu.RLock()
	active := r.active
	r.mu.RUnlock()
	return active == nil || active(module)
}

func (r *Registry) isOn(module string) bool {
	r.mu.RLock()
	on := r.on
	r.mu.RUnlock()
	return on == nil || on(module)
}

func (r *Registry) idle(deps []Dependent) bool {
	if len(deps) == 0 {
		return false
	}
	for _, d := range deps {
		if r.isOn(d.Module) {
			return false
		}
	}
	return true
}

func (r *Registry) dependents(v View) []Dependent {
	offers := r.Offers(v.Kind)
	out := []Dependent{}
	for _, n := range r.Needs() {
		if n.Kind != v.Kind || !Serves(v, offers, n.Capability) {
			continue
		}
		out = append(out, Dependent{Module: n.Module, Active: r.isActive(n.Module), Capability: n.Capability, Soft: n.Soft, Features: slices.Clone(n.Features)})
	}
	return out
}

func (r *Registry) Resolve(views []View) Dependencies {
	views = slices.Clone(views)
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	out := Dependencies{Needs: []Binding{}, Repairs: []Repair{}}
	affected := map[ID][]Binding{}
	for _, n := range r.Needs() {
		offers := r.Offers(n.Kind)
		b := Binding{Module: n.Module, Active: r.isActive(n.Module), Kind: n.Kind, Capability: n.Capability, Soft: n.Soft,
			Features: slices.Clone(n.Features), Candidates: []ID{}, State: Unconfigured}
		var bound *View
		disabled := false
		for i := range views {
			v := &views[i]
			if v.Kind != n.Kind || !Serves(*v, offers, n.Capability) {
				continue
			}
			if !v.Enabled {
				disabled = true
				continue
			}
			b.Candidates = append(b.Candidates, v.ID)
			if bound == nil {
				bound = v
			}
			if state, _ := CapabilityState(*bound, n.Capability); !Meets(state) {
				if state, _ := CapabilityState(*v, n.Capability); Meets(state) {
					bound = v
				}
			}
		}
		switch {
		case bound != nil:
			b.Connection, b.ConnectionLabel = bound.ID, bound.Label
			b.State, b.Reason = CapabilityState(*bound, n.Capability)
			if class := RepairClass(b.State); class != "" && bound.Idle == "" {
				b.Repair = RepairID(bound.ID, class)
			}
		case disabled:
			b.State = Disabled
		}
		b.Met = Meets(b.State)
		switch {
		case b.Met:
			b.Availability = FeatureAvailable
		case b.Soft:
			b.Availability = FeatureDegraded
		default:
			b.Availability = FeatureUnavailable
		}
		if !b.Met && b.Active {
			for _, id := range b.Candidates {
				affected[id] = append(affected[id], b)
			}
		}
		out.Needs = append(out.Needs, b)
	}
	for _, v := range views {
		state, reason := CapabilityState(v, "")
		class := RepairClass(state)
		if class == "" || v.Idle != "" {
			continue
		}
		rep := Repair{ID: RepairID(v.ID, class), Connection: v.ID, Label: v.Label, Class: class, State: state, Reason: reason,
			Owner: v.ConfigSource, Severity: SeverityNotice, Features: []string{}, Fix: Screen}
		if class == RepairAuth {
			rep.Hint = RepairHint(v)
		}
		for _, b := range affected[v.ID] {
			for _, f := range b.Features {
				if !slices.Contains(rep.Features, f) {
					rep.Features = append(rep.Features, f)
				}
			}
			switch {
			case !b.Soft:
				rep.Severity = SeverityBlocker
			case rep.Severity == SeverityNotice:
				rep.Severity = SeverityDegraded
			}
		}
		out.Repairs = append(out.Repairs, rep)
	}
	return out
}

func (s *Store) Dependencies(ctx context.Context) (Dependencies, error) {
	views, err := s.List(ctx)
	if err != nil {
		return Dependencies{}, err
	}
	return s.reg.Resolve(views), nil
}

func (s *Store) affectedFeatures(ctx context.Context, id ID) []string {
	if s.reg == nil {
		return nil
	}
	deps, err := s.Dependencies(ctx)
	if err != nil {
		return nil
	}
	for _, rep := range deps.Repairs {
		if rep.Connection == id {
			return rep.Features
		}
	}
	return nil
}

type Unmet struct {
	Connection ID
	Capability string
	Repair     string
	Reason     string
	Until      time.Time
}

func (e *Unmet) Error() string {
	text := fmt.Sprintf("%s from %s is unavailable: %s", e.Capability, e.Connection, e.Reason)
	if e.Repair != "" {
		text += "; repair " + e.Repair + " at " + Screen
	}
	return text
}

func Unavailable(ctx context.Context, q db.Querier, id ID, capability string, cause error) *Unmet {
	out := &Unmet{Connection: id, Capability: capability, Reason: cause.Error()}
	fallback := RepairUnreachable
	if errors.As(cause, new(config.NotConfigured)) {
		fallback = RepairConfig
	}
	v, err := ReadView(ctx, q, id)
	if err != nil {
		out.Repair = RepairID(id, RepairConfig)
		return out
	}
	state, _ := CapabilityState(v, capability)
	switch class := RepairClass(state); {
	case state == Disabled:
	case class != "":
		out.Repair = RepairID(id, class)
	default:
		out.Repair = RepairID(id, fallback)
	}
	return out
}

type OwnerMismatch struct{ IDs []ID }

func (e *OwnerMismatch) Error() string {
	ids := make([]string, len(e.IDs))
	for i, id := range e.IDs {
		ids[i] = string(id)
	}
	return "connections owned by the wrong side (host rows must be exactly the host file's connections): " + strings.Join(ids, ", ")
}

func CheckOwners(ctx context.Context, q db.Querier, host config.Host) error {
	rows, err := q.QueryContext(ctx, "SELECT id, config_source FROM connections WHERE enabled = 1 ORDER BY id")
	if err != nil {
		return err
	}
	type row struct {
		id     ID
		source string
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var out row
		return out, r.Scan(&out.id, &out.source)
	})
	if err != nil {
		return err
	}
	var wrong []ID
	for _, r := range all {
		_, declared := host.Connections[string(r.id)]
		if (r.source == SourceHost) != declared {
			wrong = append(wrong, r.id)
		}
	}
	if len(wrong) > 0 {
		return &OwnerMismatch{IDs: wrong}
	}
	return nil
}
