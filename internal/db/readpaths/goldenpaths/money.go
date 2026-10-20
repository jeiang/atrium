package readpaths_test

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/money"
)

var moneyToday = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

func init() {
	for _, p := range []readpaths.Path{
		{Name: "money.ListAccounts", Golden: "money.list_accounts", Run: func(ctx context.Context, d *db.DB) (any, error) {
			return money.ListAccounts(ctx, d.Read)
		}},
		{Name: "money.ListTransactions", Golden: "money.list_transactions", Run: func(ctx context.Context, d *db.DB) (any, error) {
			return money.ListTransactions(ctx, d.Read, money.Filter{}, false, money.DefaultList, false, moneyToday)
		}},
		{Name: "money.ListTransactions dismissed", Golden: "money.list_transactions dismissed", Run: func(ctx context.Context, d *db.DB) (any, error) {
			return money.ListTransactions(ctx, d.Read, money.Filter{}, false, money.DefaultList, true, moneyToday)
		}},
		{Name: "money.Spending month", Golden: "money.spending month", Run: func(ctx context.Context, d *db.DB) (any, error) {
			start, end, err := money.PeriodBounds("month", moneyToday)
			if err != nil {
				return nil, err
			}
			return money.Spending(ctx, d.Read, start, end, "category", "", moneyToday)
		}},
		{Name: "money.ListPayees", Golden: "money.list_payees", Run: func(ctx context.Context, d *db.DB) (any, error) {
			return money.ListPayees(ctx, d.Read, "", money.AllRows)
		}},
		{Name: "money.Bills", Golden: "money.bills", Run: func(ctx context.Context, d *db.DB) (any, error) {
			return money.Bills(ctx, d.Read, moneyToday, nil)
		}},
		{Name: "money.ListRates", Golden: "money.list_rates", Run: func(ctx context.Context, d *db.DB) (any, error) {
			return money.ListRates(ctx, d.Read, 20)
		}},
		{Name: "money.Categories", Golden: "money.categories", Run: func(ctx context.Context, d *db.DB) (any, error) {
			return money.Categories(ctx, d.Read)
		}},
		{Name: "money.TrackedSince", Golden: "money.tracked_since", Run: func(ctx context.Context, d *db.DB) (any, error) {
			since, err := money.TrackedSince(ctx, d.Read)
			if since == "" {
				return nil, err
			}
			return since, err
		}},
	} {
		readpaths.Register(p)
	}
}
