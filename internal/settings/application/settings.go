package application

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	ReadName  application.Name = "settings.read"
	SetName   application.Name = "settings.set"
	UnsetName application.Name = "settings.unset"
)

type Field = settings.Field

type FieldView = settings.FieldView

type ReadInput struct {
	All    bool
	Keys   []string
	Fields []Field
}

type Setting struct {
	Key       string  `json:"key"`
	Value     *string `json:"value"`
	Source    *string `json:"source"`
	PinSource string  `json:"pin_source,omitempty"`
	Class     string  `json:"class"`
}

type Settings struct {
	Rows   []Setting
	Fields []FieldView
}

type Value struct {
	Key   string
	Value string
	Unset bool
}

type SetInput struct {
	Values   []Value
	Validate func(ctx context.Context, q db.Querier, values map[string]string) error
	Current  func(ctx context.Context, q db.Querier) error
}

type UnsetInput struct {
	Key string
}

type Changed struct {
	Keys []string
}

func DescribeRead() application.Definition {
	return application.Describe[ReadInput, Settings](ReadName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeSet() application.Definition {
	return application.Describe[SetInput, Changed](SetName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeUnset() application.Definition {
	return application.Describe[UnsetInput, Changed](UnsetName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func refused(format string, a ...any) error {
	return &application.Error{Code: application.Refused, Message: fmt.Sprintf(format, a...)}
}

func agentRefused(ctx context.Context, key string) error {
	def, ok := settings.From(ctx).Lookup(key)
	if !ok || !def.NoAgent {
		return nil
	}
	p, marked := authapp.PrincipalOf(ctx)
	if (marked && p.Agent) || audit.SourceOf(ctx) == audit.Tool {
		return &application.Error{Code: application.Forbidden, Reason: "agent_caller", Message: key + " cannot be changed by an agent."}
	}
	return nil
}

func operatorKey(ctx context.Context, key string, writing bool) (settings.Def, error) {
	reg := settings.From(ctx)
	def, ok := reg.Lookup(key)
	if !ok {
		if host, hosted := reg.HostDefOf(key); hosted {
			if writing {
				return def, refused("%s is host configuration and is not editable here; set %s.%s in the host file", key, host.Module, host.Key)
			}
			return def, nil
		}
		if entry, known := settings.EntryFor(key); known {
			return def, refused("%s is not an operator setting; use %s", key, settings.CutoverDestination(entry))
		}
		return def, classify(settings.UnknownKey(key))
	}
	if def.IsSecret() {
		return def, refused("%s is a secret and is not read or written here", key)
	}
	return def, nil
}

func CheckRead(ctx context.Context, key string) error {
	_, err := operatorKey(ctx, key, false)
	return err
}

func (s *Service) Read(ctx context.Context, in ReadInput) (application.Read[Settings], error) {
	reg := settings.From(ctx)
	keys := in.Keys
	if in.All {
		keys = nil
		for _, def := range reg.Defs() {
			keys = append(keys, def.Key)
		}
	}
	var out Settings
	if len(keys) > 0 {
		snap, err := s.snapshot(ctx)
		if err != nil {
			return application.Read[Settings]{}, err
		}
		for _, key := range keys {
			row, err := s.row(ctx, snap, key, in.All)
			if err != nil {
				return application.Read[Settings]{}, err
			}
			out.Rows = append(out.Rows, row)
		}
	}
	if len(in.Fields) > 0 {
		views, err := settings.LoadFields(ctx, s.d.Read, in.Fields)
		if err != nil {
			return application.Read[Settings]{}, err
		}
		out.Fields = views
	}
	revision, err := s.Revision(ctx, nil)
	if err != nil {
		return application.Read[Settings]{}, err
	}
	return application.Read[Settings]{Revision: revision, Data: out}, nil
}

func (s *Service) snapshot(ctx context.Context) (*settings.Snapshot, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.Snapshot(ctx)
}

func (s *Service) row(ctx context.Context, snap *settings.Snapshot, key string, listing bool) (Setting, error) {
	if !listing {
		def, err := operatorKey(ctx, key, false)
		if err != nil {
			return Setting{}, err
		}
		if def.Key == "" {
			value, source := config.From(ctx).HostValue(key), "host"
			return Setting{Key: key, Value: &value, Source: &source, Class: "host"}, nil
		}
	}
	def, _ := settings.From(ctx).Lookup(key)
	if def.IsSecret() {
		return Setting{Key: key, Class: def.Class()}, nil
	}
	found, err := s.store.Resolve(ctx, snap, key)
	if err != nil {
		return Setting{}, err
	}
	source := string(found.Origin)
	if found.Origin == settings.FromInstance {
		source = "db"
	}
	return Setting{Key: key, Value: &found.Value, Source: &source, PinSource: found.PinSource, Class: def.Class()}, nil
}

func (s *Service) Set(ctx context.Context, in SetInput) (application.Change[Changed], error) {
	values, err := s.validated(ctx, in)
	if err != nil {
		return application.Change[Changed]{}, err
	}
	keys := make([]string, 0, len(values))
	for _, v := range values {
		keys = append(keys, v.Key)
	}
	id, err := s.command(ctx, SetName, targetOf(keys), keys, func(tx *sql.Tx) (bool, error) {
		if in.Current != nil {
			if err := in.Current(ctx, tx); err != nil {
				return false, err
			}
		}
		reg := settings.From(ctx)
		for _, v := range values {
			if s.operatorKeys {
				if _, err := operatorKey(ctx, v.Key, true); err != nil {
					return false, err
				}
			}
			if err := agentRefused(ctx, v.Key); err != nil {
				return false, err
			}
			if v.Unset {
				if err := reg.Unset(ctx, tx, v.Key); err != nil {
					return false, err
				}
				continue
			}
			if s.guard != nil {
				if err := s.guard(ctx, tx, v.Key, v.Value); err != nil {
					return false, refusedError{err: err}
				}
			}
			if err := reg.Set(ctx, tx, v.Key, v.Value); err != nil {
				return false, err
			}
		}
		return len(keys) > 0, nil
	})
	if err != nil {
		return application.Change[Changed]{}, err
	}
	return application.Change[Changed]{Data: Changed{Keys: keys}, AuditID: id}, nil
}

type PatchInput struct {
	Values   []Value
	IfMatch  string
	Validate func(context.Context, db.Querier, map[string]string) error
}

type Patched struct {
	Keys     []string             `json:"keys"`
	Revision application.Revision `json:"revision"`
}

func (s *Service) Revision(ctx context.Context, q db.Querier) (application.Revision, error) {
	counters, err := s.state.Read(ctx, q, []string{state.Settings})
	if err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(ReadName), counters, nil, "")), nil
}

func (s *Service) current(ctx context.Context, q db.Querier, ifMatch string) error {
	if strings.TrimSpace(ifMatch) == "" {
		return nil
	}
	revision, err := s.Revision(ctx, q)
	if err != nil {
		return err
	}
	if revision.Matches(ifMatch) {
		return nil
	}
	return &application.Error{Code: application.Precondition, Message: "The settings changed since they were read; reload them and try again."}
}

func (s *Service) Patch(ctx context.Context, in PatchInput) (application.Change[Patched], error) {
	keys := make([]string, 0, len(in.Values))
	for _, v := range in.Values {
		keys = append(keys, v.Key)
	}
	if err := s.current(ctx, nil, in.IfMatch); err != nil {
		details, marshalErr := detailsOf(keys)
		if marshalErr != nil {
			return application.Change[Patched]{}, marshalErr
		}
		return application.Change[Patched]{}, s.refuse(ctx, SetName, targetOf(keys), details, err)
	}
	values, err := s.validated(ctx, SetInput{Values: in.Values, Validate: in.Validate})
	if err != nil {
		return application.Change[Patched]{}, err
	}
	keys = keys[:0]
	changes := make([]settings.Change, 0, len(values))
	for _, v := range values {
		keys = append(keys, v.Key)
		changes = append(changes, settings.Change{Key: v.Key, Value: v.Value, Unset: v.Unset})
	}
	id, err := s.command(ctx, SetName, targetOf(keys), keys, func(tx *sql.Tx) (bool, error) {
		if err := s.current(ctx, tx, in.IfMatch); err != nil {
			return false, err
		}
		for _, v := range values {
			if s.operatorKeys {
				if _, err := operatorKey(ctx, v.Key, true); err != nil {
					return false, err
				}
			}
			if err := agentRefused(ctx, v.Key); err != nil {
				return false, err
			}
			if !v.Unset && s.guard != nil {
				if err := s.guard(ctx, tx, v.Key, v.Value); err != nil {
					return false, refusedError{err: err}
				}
			}
		}
		return len(changes) > 0, settings.From(ctx).SetAll(ctx, tx, changes)
	})
	if err != nil {
		return application.Change[Patched]{}, err
	}
	revision, err := s.Revision(ctx, nil)
	if err != nil {
		return application.Change[Patched]{}, err
	}
	return application.Change[Patched]{Data: Patched{Keys: keys, Revision: revision}, AuditID: id}, nil
}

func (s *Service) validated(ctx context.Context, in SetInput) ([]Value, error) {
	if in.Validate == nil {
		return in.Values, nil
	}
	values := make(map[string]string, len(in.Values))
	for _, v := range in.Values {
		values[v.Key] = v.Value
	}
	if err := in.Validate(ctx, s.d.Read, values); err != nil {
		keys := make([]string, 0, len(in.Values))
		for _, v := range in.Values {
			keys = append(keys, v.Key)
		}
		details, marshalErr := detailsOf(keys)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return nil, s.refuse(ctx, SetName, targetOf(keys), details, err)
	}
	out := make([]Value, 0, len(values))
	normalized := func(key string) Value {
		value := strings.TrimSpace(values[key])
		return Value{Key: key, Value: value, Unset: value == ""}
	}
	for _, v := range in.Values {
		out = append(out, normalized(v.Key))
		delete(values, v.Key)
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		out = append(out, normalized(key))
	}
	return out, nil
}

func targetOf(keys []string) string {
	if len(keys) == 1 {
		return keys[0]
	}
	return ""
}

func (s *Service) Unset(ctx context.Context, in UnsetInput) (application.Change[Changed], error) {
	id, err := s.command(ctx, UnsetName, in.Key, []string{in.Key}, func(tx *sql.Tx) (bool, error) {
		if _, err := operatorKey(ctx, in.Key, true); err != nil {
			return false, err
		}
		if err := agentRefused(ctx, in.Key); err != nil {
			return false, err
		}
		if err := settings.From(ctx).Unset(ctx, tx, in.Key); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return application.Change[Changed]{}, err
	}
	return application.Change[Changed]{Data: Changed{Keys: []string{in.Key}}, AuditID: id}, nil
}
