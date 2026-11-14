package module

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/accounts"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	seedKey     = "modules_seeded"
	seedActor   = "atrium"
	seedEmpty   = "empty"
	seedAdopted = "adopted"
)

func FlagHostDef(name string) settings.HostDef {
	return settings.HostDef{Key: FlagKey(name), Module: Core, Type: settings.Bool, Default: "false"}
}

var operatorData = []func(context.Context, db.Querier) (bool, error){
	settings.HasRows,
	settings.SecretsHasRows,
	accounts.ExistsAny,
	threads.HasRows,
	inference.HasRows,
}

func (s *Set) unmet(ctx context.Context, p Probe, r Declaration, states map[string]State) ([]Unmet, error) {
	var out []Unmet
	for _, rq := range r.Requires {
		reason, err := rq.Check(ctx, p)
		if err != nil {
			return nil, &requirementError{name: rq.Name, err: err}
		}
		if reason != "" {
			out = append(out, Unmet{Name: rq.Name, Reason: reason})
		}
	}
	for _, dep := range r.Needs {
		if states[dep] != Enabled {
			out = append(out, Unmet{Name: dep, Reason: "needs " + dep + " (" + string(states[dep]) + ")"})
		}
	}
	return out, nil
}

func seeded(ctx context.Context, q db.Querier) (bool, error) {
	n, err := audit.CountForKeySource(ctx, q, seedKey, string(audit.Migration))
	return n > 0, err
}

func (s *Set) Seed(ctx context.Context, d *db.DB) error {
	if done, err := seeded(ctx, d.Read); err != nil {
		return err
	} else if done {
		return s.adoptMissing(ctx, d)
	}
	ctx = audit.With(ctx, seedActor, audit.Migration)
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if done, err := seeded(ctx, tx); err != nil || done {
			return err
		}
		phaseA, err := hasOperatorData(ctx, tx)
		if err != nil {
			return err
		}
		next := seedEmpty
		if phaseA {
			next = seedAdopted
			if err := s.adopt(ctx, tx, nil); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{Key: seedKey, New: &next})
	})
}

func hasOperatorData(ctx context.Context, q db.Querier) (bool, error) {
	for _, has := range operatorData {
		found, err := has(ctx, q)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func (s *Set) adoptMissing(ctx context.Context, d *db.DB) error {
	first, err := audit.NewValueForKeySource(ctx, d.Read, seedKey, string(audit.Migration))
	if err != nil || first != seedAdopted {
		return err
	}
	pending := map[string]bool{}
	for _, r := range s.decls {
		if r.Core {
			continue
		}
		n, err := audit.CountForKey(ctx, d.Read, FlagKey(r.Name))
		if err != nil {
			return err
		}
		if n == 0 {
			pending[r.Name] = true
		}
	}
	if len(pending) == 0 {
		return nil
	}
	ctx = audit.With(ctx, seedActor, audit.Migration)
	return d.WithTx(ctx, func(tx *sql.Tx) error { return s.adopt(ctx, tx, pending) })
}

func (s *Set) adopt(ctx context.Context, q db.Querier, only map[string]bool) error {
	probe := Probe{Settings: s.reg, Q: q, Env: s.env}
	statuses, _, err := evaluate(ctx, s.decls, func(ctx context.Context, r Declaration, states map[string]State) ([]Unmet, bool, error) {
		unmet, err := s.unmet(ctx, probe, r, states)
		return unmet, len(unmet) == 0, err
	})
	if err != nil {
		return err
	}
	byName := make(map[string]Status, len(statuses))
	for _, st := range statuses {
		byName[st.Name] = st
	}
	for _, r := range s.decls {
		if r.Core {
			continue
		}
		st := byName[r.Name]
		value := "false"
		if st.State == Enabled {
			value = "true"
		}
		if only != nil && !only[r.Name] {
			continue
		}
		if settings.HostOwnedKey(FlagKey(r.Name)) {
			continue
		}
		if _, pinned, err := s.reg.Pinned(FlagKey(r.Name)); err != nil {
			return err
		} else if pinned {
			log.Info("module flag is pinned, not migrated", logx.Module, r.Name)
			continue
		}
		if err := s.reg.Set(ctx, q, FlagKey(r.Name), value); err != nil {
			return err
		}
		if st.State == Enabled {
			log.Info("module enabled by migration", logx.Module, r.Name, logx.State, string(st.State))
			continue
		}
		log.Warn("module left in needs-settings by migration", logx.Module, r.Name, logx.State, string(st.State), logx.Reason, st.Reason())
	}
	return nil
}
