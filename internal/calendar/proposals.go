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
	"github.com/jeiang/atrium/internal/py"
)

const (
	Kind    = "calendar_event"
	Account = "calendar"
	UndoOp  = "calendar_delete"
)

type ProposalError string

func (e ProposalError) Error() string { return string(e) }

type Proposals struct {
	DB  *db.DB
	Now func() float64
	Env config.Env
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

func (p *Proposals) Propose(ctx context.Context, raw py.Dict, source py.Dict, key string) (id int64, created bool, err error) {
	chosen, _ := raw.Get("calendar")
	target := Target(ICloud)
	if s, ok := chosen.(string); ok && s != "" {
		parsed, err := ParseTarget(s)
		if err != nil {
			return 0, false, ProposalError(err.Error())
		}
		target = parsed
	}
	tz, err := Zone(p.env())
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
		source = py.Dict{}
	}
	payload := py.Dict{{Key: "calendar", Value: string(target)}, {Key: "spec", Value: spec.Dict()}, {Key: "source", Value: source}}
	preview := py.Dict{
		{Key: "calendar", Value: string(target)}, {Key: "title", Value: spec.Title}, {Key: "start", Value: spec.Start.iso()},
		{Key: "end", Value: spec.End.iso()}, {Key: "all_day", Value: spec.AllDay}, {Key: "location", Value: spec.Location},
		{Key: "notes", Value: spec.Notes}, {Key: "source", Value: source},
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

func (p *Proposals) guard(ctx context.Context, target Target) error {
	var id int64
	prefix := string(target) + ":"
	err := p.DB.Read.QueryRowContext(ctx,
		"SELECT id FROM agent_writes WHERE state = 'unknown' AND substr(target, 1, length(?)) = ? ORDER BY id LIMIT 1",
		prefix, prefix).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return Error(fmt.Sprintf("calendar write %d has an unknown outcome; resolve it with `atrium writes resolve %d` before writing to the %s calendar again", id, id, target))
}

func (p *Proposals) begin(ctx context.Context, prop *proposals.Proposal, action py.Dict) (int64, error) {
	return proposals.BeginAction(ctx, p.DB.Write, prop.ID, action, p.now())
}

func (p *Proposals) settle(ctx context.Context, id int64, action, undo py.Dict, state ledger.State, text *string) error {
	var plan any
	if undo != nil {
		plan = undo
	}
	return proposals.SettleAction(ctx, p.DB.Write, id, action, plan, state, text, p.now())
}

func (p *Proposals) fail(ctx context.Context, id int64, action py.Dict, err error) error {
	text := err.Error()
	state := ledger.Failed
	if OutcomeUnknown(err) {
		state = ledger.Unknown
	}
	if settleErr := p.settle(ctx, id, action, nil, state, &text); settleErr != nil {
		return settleErr
	}
	return err
}

func (p *Proposals) settleRecord(ctx context.Context, id int64, kind string, undo py.Dict, summary string) (py.Dict, error) {
	calendarName, _ := undo.Get("calendar")
	uid, _ := undo.Get("uid")
	action := py.Dict{{Key: "type", Value: kind}, {Key: "calendar", Value: calendarName}, {Key: "uid", Value: uid}}
	if err := p.settle(ctx, id, action, undo, ledger.OK, nil); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "summary", Value: summary}}, nil
}

func (p *Proposals) Execute(ctx context.Context, prop *proposals.Proposal, _ any) (py.Dict, error) {
	target, spec, err := payloadOf(prop)
	if err != nil {
		return nil, err
	}
	env := p.env()
	tz, err := Zone(env)
	if err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	if err := p.guard(ctx, target); err != nil {
		return nil, err
	}
	action := py.Dict{{Key: "type", Value: Kind}, {Key: "calendar", Value: string(target)}, {Key: "uid", Value: spec.UID}}
	intent, err := p.begin(ctx, prop, action)
	if err != nil {
		return nil, err
	}
	ref, err := CreateEvent(ctx, env, set, target, spec, tz)
	if err != nil {
		return nil, p.fail(ctx, intent, action, err)
	}
	if err := Track(ctx, p.DB.Write, spec, target, &prop.ID, p.now()); err != nil {
		return nil, p.fail(ctx, intent, action, &Partial{Err: err})
	}
	undo := py.Dict{{Key: "op", Value: UndoOp}, {Key: "calendar", Value: string(target)}, {Key: "uid", Value: spec.UID}}
	settled := append(py.Dict{{Key: "type", Value: Kind}}, ref.Dict()...)
	if err := p.settle(ctx, intent, settled, undo, ledger.OK, nil); err != nil {
		return nil, err
	}
	return append(py.Dict{{Key: "summary", Value: fmt.Sprintf("Added to the %s calendar", target)}}, ref.Dict()...), nil
}

func (p *Proposals) Undo(ctx context.Context, plan json.RawMessage) (py.Dict, error) {
	var info struct {
		Calendar string `json:"calendar"`
		UID      string `json:"uid"`
	}
	if err := json.Unmarshal(plan, &info); err != nil {
		return nil, err
	}
	target, err := ParseTarget(info.Calendar)
	if err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	if err := p.guard(ctx, target); err != nil {
		return nil, err
	}
	if err := DeleteEvent(ctx, p.env(), set, target, info.UID); err != nil {
		return nil, err
	}
	if err := Untrack(ctx, p.DB.Write, info.UID, p.now()); err != nil {
		return nil, &Partial{Err: err}
	}
	return py.Dict{{Key: "deleted", Value: info.UID}}, nil
}

func Register(reg *proposals.Registry, p *Proposals, owner string) {
	reg.Executors[Kind] = p.Execute
	reg.Undoers[UndoOp] = p.Undo
	reg.Executors[UpdateKind] = p.ExecuteUpdate
	reg.Executors[RSVPKind] = p.ExecuteRSVP
	reg.Undoers[RestoreOp] = p.Restore
	reg.AddKind(proposals.Kind{Name: Kind, Module: owner, Label: "Calendar event", Icon: "calendar"})
	reg.AddKind(proposals.Kind{Name: UpdateKind, Module: owner, Label: "Event change", Icon: "calendar"})
	reg.AddKind(proposals.Kind{Name: RSVPKind, Module: owner, Label: "Event reply", Icon: "calendar"})
}
