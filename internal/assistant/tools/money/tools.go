// Package moneytools is the spending ledger tools of agent/tools/money.py: four reads offered in every run kind
// but nightly, and seven writes offered in chat only, each recorded in the writes log in the transaction that
// changes the ledger so the operator can undo it.
package moneytools

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
)

const (
	maxLimit = 200
	maxDays  = 365
	note     = "Amounts come from mail read by a local model; TTD and BBD are reported separately and never summed."
)

var (
	readKinds  = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}
	writeKinds = tool.Kinds{tool.Chat}
)

// Tools is TOOLS of agent/tools/money.py.
func Tools() []tool.Tool {
	return append(readTools(), writeTools()...)
}

func today(tc *tool.Context) (time.Time, error) {
	loc, err := scheduler.Zone(config.TZName(tc.Env))
	if err != nil {
		return time.Time{}, &tool.Raised{Class: "ScheduleError"}
	}
	return money.LocalDate(tc.Now(), loc), nil
}

func intArg(args py.Dict, name string, required bool) (int64, bool, error) {
	value, _ := args.Get(name)
	if value == nil {
		if required {
			return 0, false, tool.Error(name + " is required")
		}
		return 0, false, nil
	}
	switch v := value.(type) {
	case int64:
		return v, true, nil
	case *big.Int:
		return 0, false, &tool.Raised{Class: "OverflowError"}
	}
	return 0, false, tool.Error(name + " must be an integer")
}

func raised(err error) error {
	var ledger money.LedgerError
	if errors.As(err, &ledger) {
		return tool.Error(string(ledger))
	}
	var c py.Classed
	if errors.As(err, &c) {
		return &tool.Raised{Class: c.PyClass()}
	}
	return err
}

func opt[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

type spec struct {
	name, description, schema string
	handler                   tool.Handler
	effect                    tool.Effect
	kinds                     tool.Kinds
}

func build(specs []spec) []tool.Tool {
	out := make([]tool.Tool, len(specs))
	for i, s := range specs {
		out[i] = tool.Tool{
			Name: s.name, Description: s.description, InputSchema: []byte(s.schema), Handler: s.handler,
			Effect: s.effect, Contexts: s.kinds,
		}
	}
	return out
}

func write(ctx context.Context, tc *tool.Context, name string, fn func(q db.Querier) (string, *money.Change, py.Dict, error)) (any, error) {
	var result py.Dict
	var writeID *int64
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		target, change, out, err := fn(tx)
		if err != nil {
			return err
		}
		result = out
		writeID, err = change.Record(ctx, tx, money.Context{Now: tc.Now, ThreadID: &tc.ThreadID, RunID: &tc.RunID}, name, target)
		return err
	})
	if err != nil {
		return nil, raised(err)
	}
	return append(result, py.Pair{Key: "write_id", Value: opt(writeID)}), nil
}
