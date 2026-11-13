package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tasks"
)

const (
	UndoDelete  = "task_delete"
	UndoRestore = "task_restore"
)

type Service struct {
	db     *db.DB
	state  *state.Store
	ledger *ledger.Store
	now    func() float64
	env    config.Env
}

func New(d *db.DB, store *ledger.Store, now func() float64) *Service {
	return &Service{db: d, state: state.New(d), ledger: store, now: now}
}

func (s *Service) WithEnv(env config.Env) *Service {
	c := *s
	c.env = env
	return &c
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (s *Service) audit(ctx context.Context, tx *sql.Tx, name application.Name, id int64, outcome state.Outcome, effectID int64) (int64, error) {
	details, target := "", ""
	if effectID > 0 {
		details = jsonx.Dumps(map[string][]int64{"effect_ids": {effectID}}, jsonx.Opts{Compact: true})
	}
	if id > 0 {
		target = strconv.FormatInt(id, 10)
	}
	return s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: details})
}

type undoDelete struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
}

type undoRestore struct {
	Op   string     `json:"op"`
	Task tasks.Task `json:"task"`
}

func undo(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return nil, ledger.Refused("task undo payload is invalid")
	}
	switch head.Op {
	case UndoDelete:
		var u undoDelete
		if err := json.Unmarshal(payload, &u); err != nil {
			return nil, ledger.Refused("task undo payload is invalid")
		}
		if _, err := tasks.Delete(ctx, q, u.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": u.ID}, nil
	case UndoRestore:
		var u undoRestore
		if err := json.Unmarshal(payload, &u); err != nil {
			return nil, ledger.Refused("task undo payload is invalid")
		}
		if _, err := tasks.Restore(ctx, q, u.Task); err != nil {
			return nil, err
		}
		return map[string]any{"restored": u.Task.ID}, nil
	}
	return nil, ledger.Refused(fmt.Sprintf("unknown task undo %q", head.Op))
}
