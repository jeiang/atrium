package calendar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
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

func (e ProposalError) PyClass() string { return "ValueError" }

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
	sum := sha256.Sum256([]byte(py.JSONDumps(parts)))
	return hex.EncodeToString(sum[:])[:32]
}

func (p *Proposals) Propose(ctx context.Context, raw py.Dict, source py.Dict, key string) (id int64, created bool, err error) {
	chosen, _ := raw.Get("calendar")
	target := ICloud
	if py.Truthy(chosen) {
		s, ok := chosen.(string)
		if !ok || s != ICloud && s != Google {
			return 0, false, ProposalError("calendar must be one of " + TargetsText)
		}
		target = s
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
	payload := py.Dict{{Key: "calendar", Value: target}, {Key: "spec", Value: spec.Dict()}, {Key: "source", Value: source}}
	preview := py.Dict{
		{Key: "calendar", Value: target}, {Key: "title", Value: spec.Title}, {Key: "start", Value: spec.Start},
		{Key: "end", Value: spec.End}, {Key: "all_day", Value: spec.AllDay}, {Key: "location", Value: spec.Location},
		{Key: "notes", Value: spec.Notes}, {Key: "source", Value: source},
	}
	fp := fingerprint(Kind, target, spec.Title, spec.Start, spec.End)
	if key != "" {
		fp = fingerprint(Kind, key)
	}
	when := spec.Start
	if !spec.AllDay {
		when = strings.ReplaceAll(py.Clip(spec.Start, 16), "T", " ")
	}
	account := Account
	title := fmt.Sprintf("Add to calendar: %s (%s)", spec.Title, when)
	return proposals.Create(ctx, p.DB, Kind, &account, title, payload, preview, fp, p.now())
}

func payloadOf(prop *proposals.Proposal) (target string, spec Spec, err error) {
	decoded, err := py.JSONLoads(prop.Payload)
	if err != nil {
		return "", Spec{}, err
	}
	d, ok := decoded.(py.Dict)
	if !ok {
		return "", Spec{}, fmt.Errorf("%w: calendar payload is not an object", httpx.ErrType)
	}
	value, found := d.Get("calendar")
	if !found {
		return "", Spec{}, fmt.Errorf("%w: 'calendar'", httpx.ErrKey)
	}
	target, ok = value.(string)
	if !ok {
		return "", Spec{}, fmt.Errorf("%w: calendar must be a string, not %s", httpx.ErrType, py.TypeName(value))
	}
	raw, found := d.Get("spec")
	if !found {
		return "", Spec{}, fmt.Errorf("%w: 'spec'", httpx.ErrKey)
	}
	spec, err = SpecOf(raw)
	return target, spec, err
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
	ref, err := CreateEvent(ctx, env, set, target, spec, tz)
	if err != nil {
		return nil, err
	}
	if err := Track(ctx, p.DB.Write, spec, target, &prop.ID, p.now()); err != nil {
		return nil, err
	}
	action := append(py.Dict{{Key: "type", Value: Kind}}, ref.Dict()...)
	undo := py.Dict{{Key: "op", Value: UndoOp}, {Key: "calendar", Value: target}, {Key: "uid", Value: spec.UID}}
	if _, err := proposals.RecordAction(ctx, p.DB.Write, prop.ID, action, undo, true, nil, p.now()); err != nil {
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
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	if err := DeleteEvent(ctx, p.env(), set, info.Calendar, info.UID); err != nil {
		return nil, err
	}
	if err := Untrack(ctx, p.DB.Write, info.UID, p.now()); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "deleted", Value: info.UID}}, nil
}

func Register(reg *proposals.Registry, p *Proposals) {
	reg.Executors[Kind] = p.Execute
	reg.Undoers[UndoOp] = p.Undo
	reg.Executors[UpdateKind] = p.ExecuteUpdate
	reg.Executors[RSVPKind] = p.ExecuteRSVP
	reg.Undoers[RestoreOp] = p.Restore
}
