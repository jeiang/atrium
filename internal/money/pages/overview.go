package pages

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/db"

	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) showOverview(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	ctx := r.Context()
	q := s.deps.DB.Read
	day := s.today()
	start, end, err := money.PeriodBounds("month", day)
	check(err)
	spend, err := money.Spending(ctx, q, start, end, "category", "", day)
	check(err)
	unassigned, err := money.Unassigned(ctx, q, unassignedLimit, day)
	check(err)
	accounts, err := money.ListAccounts(ctx, q)
	check(err)
	soon := billsSoonDays
	bills, err := money.Bills(ctx, q, day, &soon)
	check(err)
	rates, err := money.ListRates(ctx, q, 1)
	check(err)
	s.page(w, r, http.StatusOK, "money.gohtml", overviewPage{
		Month: day.Format("January 2006"), Spending: spendingDict(spend), Unassigned: txViews(unassigned),
		Accounts: accountViews(accounts), Bills: billViews(bills), Rates: rateViews(rates),
	}, notice)
}

func (s *service) overviewPage(w http.ResponseWriter, r *http.Request) { s.showOverview(w, r, nil) }

func (s *service) transactionAccount(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "tx_id")
	accountID := f.integer("account_id")
	back := f.textOr("back", "overview")
	if f.invalid(w) {
		return
	}
	txID := ids["tx_id"]
	ok := s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		change, err := money.TransactionSetAccount(ctx, q, txID, accountID)
		if err != nil {
			return err
		}
		return s.recorded(ctx, q, change, "transaction_set_account", target("transaction", txID))
	})
	if !ok {
		return
	}
	notice := toast("success", "Assigned", "Amount resolved in the account currency.")
	if back == "transactions" {
		s.showTransactions(w, r, notice, filters{}, http.StatusOK)
		return
	}
	s.showOverview(w, r, notice)
}
