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

type BudgetExceeded string

func (e BudgetExceeded) Error() string { return string(e) }

func GetSetting(ctx context.Context, q db.Querier, key string) (string, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func SetSetting(ctx context.Context, q db.Querier, key string, value any) error {
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

func MonthlyBudget(ctx context.Context, q db.Querier) (float64, error) {
	value, ok, err := GetSetting(ctx, q, BudgetKey)
	if err != nil || !ok {
		return DefaultBudgetUSD, err
	}
	return py.ParseFloat(value)
}

type Status struct {
	BudgetUSD    float64 `json:"budget_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
	Share        float64 `json:"share"`
}

func StatusAt(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Status, error) {
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

func Check(ctx context.Context, q db.Querier, now float64, loc *time.Location, unattended bool) (Status, error) {
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
