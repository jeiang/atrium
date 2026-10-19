// Package budget is agent/budget.py: the settings table's get and set, the monthly spending limit and the
// shares of it at which unattended runs and chat pause.
package budget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/py"
)

const (
	BudgetKey        = "monthly_budget_usd"
	DefaultBudgetUSD = 75.0
	UnattendedShare  = 0.8
	ChatShare        = 1.0
)

// BudgetExceeded is the refusal to start a call once the month's spending reached the run kind's share.
type BudgetExceeded string

func (e BudgetExceeded) Error() string { return string(e) }

// GetSetting is get_setting: the stored text of key, and whether it is set.
func GetSetting(ctx context.Context, q store.Querier, key string) (string, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting is set_setting: it stores str(value). Strings, integers, floats (as Python's repr) and bools
// are accepted.
func SetSetting(ctx context.Context, q store.Querier, key string, value any) error {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case int:
		text = strconv.Itoa(v)
	case int64:
		text = strconv.FormatInt(v, 10)
	case float64:
		text = py.FloatRepr(v)
	case bool:
		text = map[bool]string{true: "True", false: "False"}[v]
	default:
		return fmt.Errorf("budget: setting %q: cannot store a %T", key, value)
	}
	_, err := q.ExecContext(ctx, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, text)
	return err
}

// MonthlyBudget is monthly_budget; a stored value that is not a float is Python's ValueError.
func MonthlyBudget(ctx context.Context, q store.Querier) (float64, error) {
	value, ok, err := GetSetting(ctx, q, BudgetKey)
	if err != nil || !ok {
		return DefaultBudgetUSD, err
	}
	return py.ParseFloat(value)
}

// Status is the month's limit, spending and the share spent.
type Status struct {
	BudgetUSD    float64 `json:"budget_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
	Share        float64 `json:"share"`
}

// StatusAt is status: the month is the one now falls in under loc, the ATRIUM_TZ zone.
func StatusAt(ctx context.Context, q store.Querier, now float64, loc *time.Location) (Status, error) {
	limit, err := MonthlyBudget(ctx, q)
	if err != nil {
		return Status{}, err
	}
	spent, err := usage.MonthToDate(ctx, q, now, loc)
	if err != nil {
		return Status{}, err
	}
	remaining := float64(limit - spent)
	if remaining < 0 {
		remaining = 0
	}
	share := 1.0
	if limit != 0 {
		share = spent / limit
	}
	return Status{BudgetUSD: limit, SpentUSD: spent, RemainingUSD: remaining, Share: share}, nil
}

// Check is check: it returns the status, or BudgetExceeded when the share spent reached the threshold of
// an unattended run (80%) or of chat (100%).
func Check(ctx context.Context, q store.Querier, now float64, loc *time.Location, unattended bool) (Status, error) {
	found, err := StatusAt(ctx, q, now, loc)
	if err != nil {
		return Status{}, err
	}
	threshold, who := ChatShare, "chat pauses at"
	if unattended {
		threshold, who = UnattendedShare, "unattended runs pause at"
	}
	if found.Share >= threshold {
		return Status{}, BudgetExceeded(fmt.Sprintf("monthly budget: $%s of $%s spent (%s); %s %s",
			py.FormatFixed(found.SpentUSD, 2), py.FormatFixed(found.BudgetUSD, 2), py.FormatPercent(found.Share, 0), who,
			py.FormatPercent(threshold, 0)))
	}
	return found, nil
}

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "budget.status",
		Golden: "budget.status",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			loc, err := store.GoldenLocation()
			if err != nil {
				return nil, err
			}
			return StatusAt(ctx, d.Read, store.GoldenNow, loc)
		},
	})
}
