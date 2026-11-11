package inference

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	BudgetKey     = "monthly_budget_usd"
	UnattendedKey = assistant.KeyBudgetUnattended
	ChatKey       = assistant.KeyBudgetChat
)

type Reason string

const (
	ReasonMonthly   Reason = "monthly"
	ReasonRun       Reason = "run"
	ReasonParentRun Reason = "parent_run"
	ReasonThread    Reason = "thread"
	ReasonSpendCap  Reason = "spend_cap"
)

type BudgetExceeded struct {
	Text      string
	Reason    Reason
	Class     Class
	NextMonth float64
}

func (e BudgetExceeded) Error() string { return e.Text }

func MonthlyBudget(ctx context.Context, q db.Querier) (float64, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, BudgetKey)
	if err != nil {
		return 0, err
	}
	return found.Float()
}

func chatShare(ctx context.Context, q db.Querier) (float64, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, ChatKey)
	if err != nil {
		return 0, err
	}
	return found.Float()
}

func unattendedShare(ctx context.Context, q db.Querier) (float64, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, UnattendedKey)
	if err != nil {
		return 0, err
	}
	return found.Float()
}

func ceiling(budget, share float64) float64 {
	return math.Min(budget*share, budget*(1-ClassificationReserveShare))
}

type Status struct {
	BudgetUSD             float64 `json:"budget_usd"`
	SpentUSD              float64 `json:"spent_usd"`
	CommittedUSD          float64 `json:"committed_usd"`
	ReservedUSD           float64 `json:"reserved_usd"`
	UnknownUSD            float64 `json:"unknown_usd"`
	ExpiredUSD            float64 `json:"expired_usd"`
	CarryInUSD            float64 `json:"carry_in_usd"`
	RemainingUSD          float64 `json:"remaining_usd"`
	Share                 float64 `json:"share"`
	InteractiveCeilingUSD float64 `json:"interactive_ceiling_usd"`
	UnattendedCeilingUSD  float64 `json:"unattended_ceiling_usd"`
	NextMonth             float64 `json:"next_month"`
}

func StatusAt(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Status, error) {
	limit, err := MonthlyBudget(ctx, q)
	if err != nil {
		return Status{}, err
	}
	chat, err := chatShare(ctx, q)
	if err != nil {
		return Status{}, err
	}
	unattended, err := unattendedShare(ctx, q)
	if err != nil {
		return Status{}, err
	}
	month := MonthStart(now, loc)
	var spent float64
	var committed, reserved, unknown, expired, carry int64
	err = q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(CASE WHEN state IN ('settled','expired') THEN cost_usd ELSE 0 END), 0), "+
			"COALESCE(SUM("+committedSQL+"), 0), "+
			"COALESCE(SUM(CASE WHEN state = 'reserved' AND expires_at > ? THEN reservation_nusd ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN state = 'unknown' AND expires_at > ? THEN reservation_nusd ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN state = 'expired' OR (state IN ('reserved','unknown') AND expires_at <= ?) THEN reservation_nusd ELSE 0 END), 0) "+
			"FROM usage WHERE month_start = ? AND budget_class <> ?",
		now, now, now, month, ClassSubscription).Scan(&spent, &committed, &reserved, &unknown, &expired)
	if err != nil {
		return Status{}, err
	}
	err = q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(reservation_nusd), 0) FROM usage WHERE month_start < ? AND state IN ('reserved','unknown') "+
			"AND expires_at > ? AND budget_class <> ?", month, now, ClassSubscription).Scan(&carry)
	if err != nil {
		return Status{}, err
	}
	committedUSD := usdOf(committed)
	carryUSD := usdOf(carry)
	remaining := limit - committedUSD - carryUSD
	if remaining < 0 {
		remaining = 0
	}
	share := 0.0
	if limit != 0 {
		share = (committedUSD + carryUSD) / limit
	}
	return Status{
		BudgetUSD:             limit,
		SpentUSD:              spent,
		CommittedUSD:          committedUSD,
		ReservedUSD:           usdOf(reserved),
		UnknownUSD:            usdOf(unknown),
		ExpiredUSD:            usdOf(expired),
		CarryInUSD:            carryUSD,
		RemainingUSD:          remaining,
		Share:                 share,
		InteractiveCeilingUSD: ceiling(limit, chat),
		UnattendedCeilingUSD:  ceiling(limit, unattended),
		NextMonth:             NextMonthStart(now, loc),
	}, nil
}

func Check(ctx context.Context, q db.Querier, now float64, loc *time.Location, unattended bool) (Status, error) {
	found, err := StatusAt(ctx, q, now, loc)
	if err != nil {
		return Status{}, err
	}
	threshold, who, class := found.InteractiveCeilingUSD, "chat pauses at", Interactive
	if unattended {
		threshold, who, class = found.UnattendedCeilingUSD, "unattended runs pause at", Unattended
	}
	if found.CommittedUSD+found.CarryInUSD >= threshold {
		return Status{}, BudgetExceeded{
			Text: fmt.Sprintf("monthly budget: $%s of $%s spent (%s); %s %s",
				strconv.FormatFloat(found.CommittedUSD+found.CarryInUSD, 'f', 2, 64), strconv.FormatFloat(found.BudgetUSD, 'f', 2, 64),
				strconv.FormatFloat(found.Share*100, 'f', 0, 64)+"%", who,
				strconv.FormatFloat(threshold/found.BudgetUSD*100, 'f', 0, 64)+"%"),
			Reason:    ReasonMonthly,
			Class:     class,
			NextMonth: found.NextMonth,
		}
	}
	return found, nil
}

const RunCapPrefix = "max_run_cost_usd_"

func RunCapKey(model string) string {
	return RunCapPrefix + strings.ReplaceAll(strings.TrimPrefix(model, "claude-"), "-", "_")
}

func RunCap(ctx context.Context, q db.Querier, model string) (float64, error) {
	if _, err := pricing.Lookup(model); err != nil {
		return 0, err
	}
	found, err := settings.From(ctx).Resolve(ctx, q, RunCapKey(model))
	if err != nil {
		return 0, err
	}
	return found.Float()
}

const SpendCapText = "Anthropic spend limit reached: the organization's API usage cap refused the request. Raise the cap or wait for it to reset in the Anthropic console."

func SpendCap() BudgetExceeded {
	return BudgetExceeded{Text: SpendCapText, Reason: ReasonSpendCap}
}

func IsCapError(text string) bool {
	return text == SpendCapText || strings.HasPrefix(text, "run cost cap:") ||
		strings.HasPrefix(text, "parent run cost cap:") || strings.HasPrefix(text, "thread cost cap:") ||
		strings.HasPrefix(text, "run cost ceiling reached")
}

const ThreadCapKey = "thread_cost_cap_usd"

func ThreadCap(ctx context.Context, q db.Querier, scope ThreadScope) (spent, limit float64, own bool, err error) {
	found, err := settings.From(ctx).Resolve(ctx, q, ThreadCapKey)
	if err != nil {
		return 0, 0, false, err
	}
	if limit, err = found.Float(); err != nil {
		return 0, 0, false, err
	}
	n, err := CommittedOnThreads(ctx, q, scope.IDs)
	if err != nil {
		return 0, 0, false, err
	}
	spent = usdOf(n)
	if scope.CapUSD != nil && *scope.CapUSD < limit {
		limit, own = *scope.CapUSD, true
	}
	return spent, limit, own, nil
}

func CommittedOnThreads(ctx context.Context, q db.Querier, threadIDs []int64) (int64, error) {
	if len(threadIDs) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(threadIDs)), ",")
	args := make([]any, len(threadIDs))
	for i, id := range threadIDs {
		args[i] = id
	}
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM("+committedSQL+"), 0) FROM usage WHERE thread_id IN ("+marks+")", args...).Scan(&n)
	return n, err
}

func registerRunCaps() []settings.Def {
	var defs []settings.Def
	for _, m := range pricing.Models() {
		defs = append(defs, settings.Value(settings.UI, settings.Def{
			Key: RunCapKey(m.ID), Label: "Run cost cap, " + m.Name + " (USD)", Module: "assistant", Type: settings.Float, Default: strconv.FormatFloat(m.RunCapUSD, 'f', -1, 64),
			Check: settings.Above(0)}))
	}
	return defs
}

func init() {
	settings.MustRegister(registerRunCaps()...)
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{
			Key: BudgetKey, Label: "Monthly budget (USD)", Module: "assistant", Type: settings.Float, Default: "150",
			Check: settings.Above(0)}),
		settings.Value(settings.UI, settings.Def{
			Key: UnattendedKey, Label: "Scheduled runs stop at this share of the budget", Module: "assistant", Type: settings.Float, Default: "0.8",
			Check: settings.Within(0, 1), Peers: assistant.Ordered(nil, []string{ChatKey})}),
		settings.Value(settings.UI, settings.Def{
			Key: ThreadCapKey, Label: "Thread cost cap (USD)", Module: "assistant", Type: settings.Float, Default: "10",
			Check: settings.Above(0)}),
	)
}
