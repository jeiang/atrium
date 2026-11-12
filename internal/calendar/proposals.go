package calendar

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/proposals"
)

const (
	Kind    = "calendar_event"
	Account = "calendar"
	UndoOp  = "calendar_delete"
)

type ProposalError string

func (e ProposalError) Error() string { return string(e) }

type specPayload struct {
	UID      string `json:"uid"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"all_day"`
	Location string `json:"location"`
	Notes    string `json:"notes"`
}

func (s Spec) payload() specPayload {
	return specPayload{UID: s.UID, Title: s.Title, Start: s.Start.iso(), End: s.End.iso(), AllDay: s.AllDay, Location: s.Location, Notes: s.Notes}
}

type proposalPayload struct {
	Calendar string         `json:"calendar"`
	Spec     specPayload    `json:"spec"`
	Source   map[string]any `json:"source"`
}

type proposalPreview struct {
	Calendar string         `json:"calendar"`
	Title    string         `json:"title"`
	Start    string         `json:"start"`
	End      string         `json:"end"`
	AllDay   bool           `json:"all_day"`
	Location string         `json:"location"`
	Notes    string         `json:"notes"`
	Source   map[string]any `json:"source"`
}

type actionPayload struct {
	Type     string `json:"type"`
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
	Response string `json:"response,omitempty"`
}

type undoPlan struct {
	Op       string `json:"op"`
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
	ICS      string `json:"ics,omitempty"`
	Google   string `json:"google,omitempty"`
}

type ExecuteResult struct {
	Summary  string `json:"summary"`
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
}

type SummaryResult struct {
	Summary string `json:"summary"`
}

type RestoreResult struct {
	Restored string `json:"restored"`
}

type Proposals struct {
	DB     *db.DB
	Ledger *ledger.Store
	Now    func() float64
	Env    config.Env
}

func (p *Proposals) now() float64 {
	if p.Now != nil {
		return p.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (p *Proposals) env() config.Env {
	if p.Env != nil {
		return p.Env
	}
	return config.OS
}

func fingerprint(parts ...any) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		encoded, _ := json.Marshal(part)
		quoted[i] = string(encoded)
	}
	sum := sha256.Sum256([]byte("[" + strings.Join(quoted, ", ") + "]"))
	return hex.EncodeToString(sum[:])[:32]
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func (p *Proposals) Propose(ctx context.Context, raw map[string]any, source map[string]any, key string) (id int64, created bool, err error) {
	target := Target(ICloud)
	if s, ok := raw["calendar"].(string); ok && s != "" {
		parsed, err := ParseTarget(s)
		if err != nil {
			return 0, false, ProposalError(err.Error())
		}
		target = parsed
	}
	tz, err := Zone(ctx, p.env())
	if err != nil {
		return 0, false, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return 0, false, err
	}
	spec, err := NormalizeSpec(raw, tz, "", set.DefaultMinutes)
	if err != nil {
		if _, ok := err.(Error); ok {
			return 0, false, ProposalError(err.Error())
		}
		return 0, false, err
	}
	if source == nil {
		source = map[string]any{}
	}
	payload := proposalPayload{Calendar: string(target), Spec: spec.payload(), Source: source}
	preview := proposalPreview{
		Calendar: string(target), Title: spec.Title, Start: spec.Start.iso(),
		End: spec.End.iso(), AllDay: spec.AllDay, Location: spec.Location,
		Notes: spec.Notes, Source: source,
	}
	fp := fingerprint(Kind, string(target), spec.Title, spec.Start.iso(), spec.End.iso(), spec.Location, spec.Notes)
	if key != "" {
		fp = fingerprint(Kind, key)
	}
	when := spec.Start.iso()
	if !spec.AllDay {
		when = strings.ReplaceAll(clip(spec.Start.iso(), 16), "T", " ")
	}
	account := Account
	title := fmt.Sprintf("Add to calendar: %s (%s)", spec.Title, when)
	return proposals.Create(ctx, p.DB, Kind, &account, title, payload, preview, fp, p.now())
}

func payloadOf(prop *proposals.Proposal) (target Target, spec Spec, err error) {
	var d map[string]any
	if err := json.Unmarshal(prop.Payload, &d); err != nil {
		return "", Spec{}, err
	}
	value, found := d["calendar"]
	if !found {
		return "", Spec{}, fmt.Errorf("%w: 'calendar'", httpx.ErrMissing)
	}
	text, ok := value.(string)
	if !ok {
		return "", Spec{}, fmt.Errorf("%w: calendar must be a string, not %T", httpx.ErrShape, value)
	}
	if target, err = ParseTarget(text); err != nil {
		return "", Spec{}, err
	}
	raw, found := d["spec"]
	if !found {
		return "", Spec{}, fmt.Errorf("%w: 'spec'", httpx.ErrMissing)
	}
	spec, err = SpecOf(raw)
	return target, spec, err
}

func (p *Proposals) guard(ctx context.Context, target Target, uid string) error {
	id, err := ledger.BlockedTarget(ctx, p.DB.Read, string(target)+":"+uid)
	if err != nil {
		return err
	}
	if id == 0 {
		return nil
	}
	return Error(fmt.Sprintf("calendar write %d has an unknown outcome; resolve it with `atrium effects resolve %d --outcome applied` before writing to the %s calendar again", id, id, target))
}

func encode(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (p *Proposals) begin(ctx context.Context, prop *proposals.Proposal, operation string, target Target, uid string,
	action, before, undo any) (ledger.Ticket, error) {
	key := string(target) + ":" + uid
	body, err := encode(action)
	if err != nil {
		return ledger.Ticket{}, err
	}
	prior, err := encode(before)
	if err != nil {
		return ledger.Ticket{}, err
	}
	plan, err := encode(undo)
	if err != nil {
		return ledger.Ticket{}, err
	}
	return p.Ledger.Begin(ctx, ledger.New{
		Origin: "calendar", Operation: operation, OperationKey: operation + ":" + ledger.NewKey(),
		Target: key, ProposalID: &prop.ID, Action: body, Before: prior, Undo: plan,
		Resources: []ledger.Resource{{Key: "calendar:" + key}},
	}, nil)
}

func (p *Proposals) fail(ctx context.Context, ticket ledger.Ticket, err error) error {
	state := ledger.Failed
	if OutcomeUnknown(err) {
		state = ledger.Unknown
	}
	if settleErr := p.Ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: state, Error: err.Error()}, nil); settleErr != nil {
		return settleErr
	}
	return err
}

func (p *Proposals) settleRecord(ctx context.Context, ticket ledger.Ticket, action any, undo undoPlan, summary string) (SummaryResult, error) {
	after, err := encode(action)
	if err != nil {
		return SummaryResult{}, err
	}
	plan, err := encode(undo)
	if err != nil {
		return SummaryResult{}, err
	}
	if err := p.Ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Done, After: after, Undo: plan}, nil); err != nil {
		return SummaryResult{}, err
	}
	return SummaryResult{Summary: summary}, nil
}

func (p *Proposals) Execute(ctx context.Context, prop *proposals.Proposal, _ any) (any, error) {
	target, spec, err := payloadOf(prop)
	if err != nil {
		return nil, err
	}
	env := p.env()
	tz, err := Zone(ctx, env)
	if err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	if err := p.guard(ctx, target, spec.UID); err != nil {
		return nil, err
	}
	action := actionPayload{Type: Kind, Calendar: string(target), UID: spec.UID}
	undo := undoPlan{Op: UndoOp, Calendar: string(target), UID: spec.UID}
	ticket, err := p.begin(ctx, prop, Kind, target, spec.UID, action, nil, undo)
	if err != nil {
		return nil, err
	}
	ref, err := CreateEvent(ctx, env, set, target, spec, tz)
	if err != nil {
		return nil, p.fail(ctx, ticket, err)
	}
	settled := actionPayload{Type: Kind, Calendar: ref.Calendar, UID: ref.UID}
	after, err := encode(settled)
	if err != nil {
		return nil, err
	}
	plan, err := encode(undo)
	if err != nil {
		return nil, err
	}
	err = p.Ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Done, After: after, Undo: plan},
		func(ctx context.Context, tx *sql.Tx) error {
			return Track(ctx, tx, spec, target, &prop.ID, p.now())
		})
	if err != nil {
		return nil, err
	}
	return ExecuteResult{Summary: fmt.Sprintf("Added to the %s calendar", target), Calendar: ref.Calendar, UID: ref.UID}, nil
}

func (p *Proposals) Proposals(owner string) []proposals.Definition {
	return []proposals.Definition{
		{Kind: proposals.Kind{Name: Kind, Module: owner, Label: "Calendar event", Icon: "calendar"}, Execute: p.Execute},
		{Kind: proposals.Kind{Name: UpdateKind, Module: owner, Label: "Event change", Icon: "calendar"}, Execute: p.ExecuteUpdate},
		{Kind: proposals.Kind{Name: RSVPKind, Module: owner, Label: "Event reply", Icon: "calendar"}, Execute: p.ExecuteRSVP},
	}
}

func Register(reg *proposals.Registry, p *Proposals, owner string) {
	for _, d := range p.Proposals(owner) {
		reg.Add(d)
	}
}

func (p *Proposals) Undo() []ledger.UndoDefinition {
	return []ledger.UndoDefinition{{Operation: RestoreOp, Undoer: ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var info undoPlan
			if err := json.Unmarshal(raw, &info); err != nil || info.Calendar == "" || info.UID == "" {
				return ledger.Refused("calendar undo payload is invalid")
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			value, err := p.Restore(ctx, raw)
			if err != nil {
				var failed Error
				switch {
				case OutcomeUnknown(err):
					return ledger.Result{State: ledger.Unknown, Error: err.Error()}, nil
				case errors.As(err, &failed):
					return ledger.Result{State: ledger.Failed, Error: err.Error()}, nil
				}
				return ledger.Result{}, err
			}
			after, err := encode(value)
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: after}, nil
		},
	}}}
}

func RegisterLedgerUndoer(s *ledger.Store, p *Proposals) error {
	for _, d := range p.Undo() {
		if err := s.RegisterUndo(d.Operation, d.Undoer); err != nil {
			return err
		}
	}
	return nil
}
