package budget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	BudgetKey     = "monthly_budget_usd"
	UnattendedKey = assistant.KeyBudgetUnattended
	ChatKey       = assistant.KeyBudgetChat
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
	found, err := settings.Default.Resolve(ctx, q, BudgetKey, 0)
	if err != nil {
		return 0, err
	}
	return found.Float()
}

func chatShare(ctx context.Context, q db.Querier) (float64, error) {
	found, err := settings.Default.Resolve(ctx, q, ChatKey, 0)
	if err != nil {
		return 0, err
	}
	return found.Float()
}

func unattendedShare(ctx context.Context, q db.Querier) (float64, error) {
	found, err := settings.Default.Resolve(ctx, q, UnattendedKey, 0)
	if err != nil {
		return 0, err
	}
	return found.Float()
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
	read, who := chatShare, "chat pauses at"
	if unattended {
		read, who = unattendedShare, "unattended runs pause at"
	}
	threshold, err := read(ctx, q)
	if err != nil {
		return Status{}, err
	}
	if found.Share >= threshold {
		return Status{}, BudgetExceeded(fmt.Sprintf("monthly budget: $%s of $%s spent (%s); %s %s",
			py.FormatFixed(found.SpentUSD, 2), py.FormatFixed(found.BudgetUSD, 2), py.FormatPercent(found.Share, 0), who,
			py.FormatPercent(threshold, 0)))
	}
	return found, nil
}

const RunCapPrefix = "max_run_cost_usd_"

var runCapDefaults = map[string]string{
	pricing.HaikuID:  "2",
	pricing.SonnetID: "6",
	pricing.OpusID:   "10",
}

func RunCapKey(model string) string {
	return RunCapPrefix + strings.ReplaceAll(strings.TrimPrefix(model, "claude-"), "-", "_")
}

func RunCap(ctx context.Context, q db.Querier, model string) (float64, error) {
	if _, err := pricing.Lookup(model); err != nil {
		return 0, err
	}
	found, err := settings.Default.Resolve(ctx, q, RunCapKey(model), 0)
	if err != nil {
		return 0, err
	}
	return found.Float()
}

func ColdRequestUSD(model string, tokens int64, ttl string) (float64, error) {
	write := pricing.Tokens{CacheWrite5m: tokens}
	if ttl == prompts.TTL1h {
		write = pricing.Tokens{CacheWrite1h: tokens}
	}
	return pricing.CostUSD(model, write, 0)
}

func UncachedRequestUSD(model string, input, output int64) (float64, error) {
	return pricing.CostUSD(model, pricing.Tokens{Input: input, Output: output}, 0)
}

const SpendCapText = "Anthropic spend limit reached: the organization's API usage cap refused the request. Raise the cap or wait for it to reset in the Anthropic console."

func SpendCap() BudgetExceeded { return BudgetExceeded(SpendCapText) }

func registerRunCaps() []settings.Def {
	var defs []settings.Def
	for _, id := range pricing.IDs() {
		defs = append(defs, settings.Def{
			Key: RunCapKey(id), Module: "assistant", Type: settings.Float, Default: runCapDefaults[id],
			Check: settings.Above(0), Class: settings.Cap, Scopes: []settings.Scope{settings.Instance},
		})
	}
	return defs
}

func init() {
	settings.MustRegister(registerRunCaps()...)
	settings.MustRegister(
		settings.Def{
			Key: BudgetKey, Module: "assistant", Type: settings.Float, Default: "150",
			Check: settings.Above(0), Class: settings.UI, Scopes: []settings.Scope{settings.Instance},
		},
		settings.Def{
			Key: UnattendedKey, Module: "assistant", Type: settings.Float, Default: "0.8",
			Check: settings.Within(0, 1), Peers: assistant.Ordered(false, nil, []string{ChatKey}),
			Class: settings.Advanced, Scopes: []settings.Scope{settings.Instance},
		},
	)
}
