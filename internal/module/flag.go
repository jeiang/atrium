package module

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	seedKey     = "modules_seeded"
	seedActor   = "atrium"
	seedEmpty   = "empty"
	seedAdopted = "adopted"
)

func init() {
	for _, name := range []string{Mail, Calendar, Notion, Money, Media, Assistant, Notifications, Grafana, Contacts} {
		settings.MustRegister(FlagDef(name))
	}
}

func FlagOn(ctx context.Context, q db.Querier, name string) (bool, error) {
	got, err := settings.Default.Resolve(ctx, q, FlagKey(name), 0)
	return got.Value == "true", err
}

var operatorTables = []string{"settings", "secrets", "accounts", "threads", "usage"}

func (s *Set) unmet(ctx context.Context, p Probe, r Registration, states map[string]State) ([]Unmet, error) {
	var out []Unmet
	for _, rq := range r.Requires {
		reason, err := rq.Check(ctx, p)
		if err != nil {
			return nil, err
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
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM settings_audit WHERE key = ? AND source = ?", seedKey, string(audit.Migration)).Scan(&n)
	return n > 0, err
}

var lateModules = []string{Contacts}

func (s *Set) Seed(ctx context.Context, d *db.DB) error {
	if done, err := seeded(ctx, d.Read); err != nil {
		return err
	} else if done {
		return s.adoptLate(ctx, d)
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
	for _, t := range operatorTables {
		var one int
		err := q.QueryRowContext(ctx, "SELECT 1 FROM "+t+" LIMIT 1").Scan(&one)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return false, nil
}

func (s *Set) adoptLate(ctx context.Context, d *db.DB) error {
	var first string
	err := d.Read.QueryRowContext(ctx, "SELECT new_value FROM settings_audit WHERE key = ? AND source = ? ORDER BY id LIMIT 1",
		seedKey, string(audit.Migration)).Scan(&first)
	if err != nil || first != seedAdopted {
		return err
	}
	pending := map[string]bool{}
	for _, name := range lateModules {
		var n int
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings_audit WHERE key = ?", FlagKey(name)).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			pending[name] = true
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
	states := map[string]State{}
	for _, r := range s.regs {
		if r.Core {
			states[r.Name] = Enabled
			continue
		}
		unmet, err := s.unmet(ctx, probe, r, states)
		if err != nil {
			return err
		}
		value, state := "false", NeedsSettings
		if len(unmet) == 0 {
			value, state = "true", Enabled
		}
		states[r.Name] = state
		if only != nil && !only[r.Name] {
			continue
		}
		if _, pinned, err := s.reg.Pinned(FlagKey(r.Name)); err != nil {
			return err
		} else if pinned {
			log.Info("module flag is pinned, not migrated", logx.Module, r.Name)
			continue
		}
		if err := s.reg.Set(ctx, q, FlagKey(r.Name), value, 0); err != nil {
			return err
		}
		if state == Enabled {
			log.Info("module enabled by migration", logx.Module, r.Name, logx.State, string(state))
			continue
		}
		log.Warn("module left in needs-settings by migration", logx.Module, r.Name, logx.State, string(state), logx.Reason, joinReasons(unmet))
	}
	return nil
}
