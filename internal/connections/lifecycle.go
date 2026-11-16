package connections

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	ReasonUnitOff     = "its app is off"
	ReasonOperatorOff = "turned off by the operator"
)

type Refusal struct {
	ID     ID
	Reason string
}

func (e *Refusal) Error() string { return "connections: " + string(e.ID) + " " + e.Reason }

func (r *Registry) TouchUnits() {
	if r == nil {
		return
	}
	select {
	case r.units <- struct{}{}:
	default:
	}
}

func (r *Registry) UnitsChanged() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.units
}

func (s *Store) SyncUnits(ctx context.Context) error {
	if s.d == nil || s.reg == nil {
		return nil
	}
	views, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, v := range views {
		if !builtinIDs[string(v.ID)] || v.ConfigSource != SourceOperator || len(v.Dependents) == 0 {
			continue
		}
		off := s.reg.idle(v.Dependents)
		var res sql.Result
		switch {
		case off && v.Enabled:
			res, err = s.d.Write.ExecContext(ctx,
				"UPDATE connections SET enabled = 0, state = 'disabled', reason = ?, revision = revision + 1, updated_at = ? "+
					"WHERE id = ? AND enabled = 1 AND config_source = 'operator'",
				ReasonUnitOff, s.stamp(), v.ID)
		case !off && !v.Enabled && v.State == Disabled && v.Reason == ReasonUnitOff:
			res, err = s.d.Write.ExecContext(ctx,
				"UPDATE connections SET enabled = 1, state = 'untested', reason = '', checked_at = NULL, results_json = '{}', "+
					"revision = revision + 1, updated_at = ? WHERE id = ? AND enabled = 0 AND state = 'disabled' AND reason = ? AND config_source = 'operator'",
				s.stamp(), v.ID, ReasonUnitOff)
		default:
			continue
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			state := "enabled"
			if off {
				state = "disabled"
			}
			log.Info("a built-in connection follows its apps", logx.Integration, string(v.ID), logx.State, state)
		}
	}
	return nil
}

func (s *Store) SetEnabled(ctx context.Context, id ID, enabled bool, commits ...Commit) (View, error) {
	if !audit.HasActor(ctx) {
		return View{}, audit.ErrNoActor
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if current.ConfigSource == SourceHost {
		return View{}, &Refusal{ID: id, Reason: "is declared by the host; change it in the host configuration"}
	}
	if current.Enabled == enabled {
		if err := s.d.WithTx(ctx, func(tx *sql.Tx) error { return committed(ctx, tx, false, commits) }); err != nil {
			return View{}, err
		}
		return current, nil
	}
	if enabled {
		if _, ok := s.reg.Lookup(current.Kind); !ok {
			return View{}, &Refusal{ID: id, Reason: "has a kind this version does not support"}
		}
		if current.Reason == ReasonUnitOff && s.reg.idle(current.Dependents) {
			return View{}, &Refusal{ID: id, Reason: "is off because its app is off; turn the app on instead"}
		}
	}
	old, next := "enabled", "disabled"
	if enabled {
		old, next = next, old
	}
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if enabled {
			res, err = tx.ExecContext(ctx,
				"UPDATE connections SET enabled = 1, state = CASE WHEN state = 'unconfigured' THEN 'unconfigured' ELSE 'untested' END, reason = '', "+
					"checked_at = NULL, results_json = '{}', revision = revision + 1, updated_at = ? WHERE id = ? AND enabled = 0",
				s.stamp(), id)
		} else {
			res, err = tx.ExecContext(ctx,
				"UPDATE connections SET enabled = 0, state = 'disabled', reason = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND enabled = 1",
				ReasonOperatorOff, s.stamp(), id)
		}
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err == nil {
				err = &Conflict{ID: id, Revision: current.revision}
			}
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Key: "connection." + string(id) + ".enabled", Old: &old, New: &next}); err != nil {
			return err
		}
		return committed(ctx, tx, true, commits)
	})
	if err != nil {
		return View{}, err
	}
	return s.afterWrite(ctx, id)
}

func (s *Store) UnitsChanged() <-chan struct{} { return s.reg.UnitsChanged() }
