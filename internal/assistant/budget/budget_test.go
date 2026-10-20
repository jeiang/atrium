package budget_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/settings"
)

const now = 1_780_000_000.0

var spain = func() *time.Location {
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		panic(err)
	}
	return loc
}()

func spend(t *testing.T, d *db.DB, cost, ts float64) {
	t.Helper()
	ctx := t.Context()
	if _, err := d.Write.ExecContext(ctx, "INSERT OR IGNORE INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'medium', ?, ?)", ts, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, "INSERT OR IGNORE INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'medium', 'done', ?)", ts); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx,
		"INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "+
			"VALUES (?, 1, 1, 'chat', 'm', 'medium', 0, 0, 0, 0, 0, 0, '<=100K', ?)", ts, cost); err != nil {
		t.Fatal(err)
	}
}

func exceeded(t *testing.T, err error) budget.BudgetExceeded {
	t.Helper()
	var be budget.BudgetExceeded
	if !errors.As(err, &be) {
		t.Fatalf("error %v, want BudgetExceeded", err)
	}
	return be
}

func TestThresholds(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	if got, err := budget.MonthlyBudget(ctx, d.Read); err != nil || got != 75 {
		t.Fatalf("default budget = %v, %v", got, err)
	}
	spend(t, d, 60, now)
	if _, err := budget.Check(ctx, d.Write, now, spain, false); err != nil {
		t.Errorf("chat at 80%%: %v", err)
	}
	err := func() error { _, err := budget.Check(ctx, d.Write, now, spain, true); return err }()
	msg := exceeded(t, err)
	if want := "monthly budget: $60.00 of $75.00 spent (80%); unattended runs pause at 80%"; string(msg) != want {
		t.Errorf("message %q, want %q", msg, want)
	}
	spend(t, d, 15, now)
	_, err = budget.Check(ctx, d.Write, now, spain, false)
	if want := "monthly budget: $75.00 of $75.00 spent (100%); chat pauses at 100%"; string(exceeded(t, err)) != want {
		t.Errorf("chat message %q, want %q", err, want)
	}
	if err := budget.SetSetting(ctx, d.Write, budget.BudgetKey, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Check(ctx, d.Write, now, spain, true); err != nil {
		t.Errorf("after raising the budget: %v", err)
	}
	st, err := budget.StatusAt(ctx, d.Write, now, spain)
	if err != nil || st.RemainingUSD != 125 || st.BudgetUSD != 200 || st.SpentUSD != 75 {
		t.Errorf("status %+v, %v", st, err)
	}
}

func TestExhaustedBudgetRefusesUnattendedAt80AndChatAt100(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	if err := budget.SetSetting(ctx, d.Write, budget.BudgetKey, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Check(ctx, d.Write, now, spain, true); err == nil {
		t.Error("an unattended run started with a zero budget")
	}
	if _, err := budget.Check(ctx, d.Write, now, spain, false); err == nil {
		t.Error("chat started with a zero budget")
	}
	st, err := budget.StatusAt(ctx, d.Write, now, spain)
	if err != nil || st.Share != 1 || st.RemainingUSD != 0 {
		t.Errorf("a zero budget is fully spent: %+v, %v", st, err)
	}
}

func TestSpendingBetweenTheSharesPausesOnlyUnattendedRuns(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	spend(t, d, 74.99, now)
	if _, err := budget.Check(ctx, d.Write, now, spain, true); err == nil {
		t.Error("unattended run allowed at 99.99%")
	}
	if _, err := budget.Check(ctx, d.Write, now, spain, false); err != nil {
		t.Errorf("chat refused at 99.99%%: %v", err)
	}
	spend(t, d, 0.01, now)
	if _, err := budget.Check(ctx, d.Write, now, spain, false); err == nil {
		t.Error("chat allowed at 100%")
	}
}

func TestBudgetCountsOnlyTheZonesMonth(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	spend(t, d, 70, float64(time.Date(2026, 10, 31, 23, 0, 0, 0, tokyo).Unix()))
	at := float64(time.Date(2026, 10, 31, 20, 0, 0, 0, spain).Unix())
	if _, err := budget.Check(ctx, d.Write, at, spain, true); err == nil {
		t.Error("the October spend is not counted in October in Port of Spain")
	}
	if got, err := budget.StatusAt(ctx, d.Write, at, tokyo); err != nil || got.SpentUSD != 0 {
		t.Errorf("under Asia/Tokyo it is November already: %+v, %v", got, err)
	}
}

func TestSettings(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	if _, ok, err := budget.GetSetting(ctx, d.Read, "k"); err != nil || ok {
		t.Fatalf("unset setting: %v, %v", ok, err)
	}
	for value, want := range map[any]string{"x": "x", 200: "200", int64(7): "7", 200.0: "200.0", 1e16: "1e+16", true: "True"} {
		if err := budget.SetSetting(ctx, d.Write, "k", value); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := budget.GetSetting(ctx, d.Read, "k"); err != nil || !ok || got != want {
			t.Errorf("SetSetting(%v) stored %q, %v, %v; want %q", value, got, ok, err, want)
		}
	}
	if err := budget.SetSetting(ctx, d.Write, "k", []string{"a"}); err == nil {
		t.Error("a list was stored")
	}
	if err := budget.SetSetting(ctx, d.Write, budget.BudgetKey, "lots"); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.MonthlyBudget(ctx, d.Read); err == nil || err.Error() != "could not convert string to float: 'lots'" {
		t.Errorf("a stored budget that is not a number: %v", err)
	}
}

func TestBudgetWrittenThroughTheRegistryIsWhatTheReadersUse(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	spend(t, d, 75, now)
	if _, err := budget.Check(ctx, d.Write, now, spain, false); err == nil {
		t.Fatal("the default budget of 75 allows chat at 100%")
	}
	if err := settings.Default.Set(ctx, d.Write, budget.BudgetKey, "200", 0); err != nil {
		t.Fatal(err)
	}
	if got, err := budget.MonthlyBudget(ctx, d.Read); err != nil || got != 200 {
		t.Errorf("budget after the write = %v, %v", got, err)
	}
	if _, err := budget.Check(ctx, d.Write, now, spain, false); err != nil {
		t.Errorf("chat after raising the budget: %v", err)
	}
	if err := settings.Default.Set(ctx, d.Write, budget.UnattendedKey, "0.3", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Check(ctx, d.Write, now, spain, true); err == nil {
		t.Error("unattended runs at 37% of the budget pass a 30% share")
	}
}

type pinned map[string]string

func (p pinned) Pin(key string) (string, string, bool) {
	v, ok := p[key]
	return v, "test pin", ok
}

func TestPinnedBudgetBeatsTheDatabaseAndRefusesWrites(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	if err := budget.SetSetting(ctx, d.Write, budget.BudgetKey, 200); err != nil {
		t.Fatal(err)
	}
	settings.Default.SetPins(pinned{budget.BudgetKey: "10"})
	t.Cleanup(func() { settings.Default.SetPins(nil) })
	if got, err := budget.MonthlyBudget(ctx, d.Read); err != nil || got != 10 {
		t.Errorf("pinned budget = %v, %v", got, err)
	}
	var refused *settings.PinnedError
	if err := settings.Default.Set(ctx, d.Write, budget.BudgetKey, "50", 0); !errors.As(err, &refused) || refused.Source != "test pin" {
		t.Errorf("write over a pin: %v", err)
	}
}

func TestBudgetValidationMessageIsTheSameForEveryCaller(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	const want = "monthly_budget_usd must be greater than 0"
	for _, value := range []string{"0", "-5"} {
		form := settings.Default.Validate(budget.BudgetKey, value)
		cli := settings.Default.Set(ctx, d.Write, budget.BudgetKey, value, 0)
		if form == nil || cli == nil || form.Error() != want || cli.Error() != want {
			t.Errorf("value %q: validate %v, set %v, want %q", value, form, cli, want)
		}
	}
	if err := settings.Default.Set(ctx, d.Write, budget.BudgetKey, "lots", 0); err == nil || err.Error() != "monthly_budget_usd must be a number" {
		t.Errorf("non-number: %v", err)
	}
	if got, err := budget.MonthlyBudget(ctx, d.Read); err != nil || got != 75 {
		t.Errorf("refused writes changed the budget to %v, %v", got, err)
	}
}
