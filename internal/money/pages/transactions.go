package pages

import (
	"context"
	"net/http"

	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) showTransactions(w http.ResponseWriter, r *http.Request, notice *tpl.Toast, f filters, status int) {
	ctx := r.Context()
	q := s.deps.DB.Read
	rows, err := money.ListTransactions(ctx, q, money.Filter{
		Start: f.Start, End: f.End, AccountID: f.AccountID, Payee: f.Payee, Category: f.Category, Kind: f.Kind, Currency: f.Currency,
	}, f.Unassigned, transactionLimit, f.Dismissed, s.today())
	check(err)
	accounts, err := money.ListAccounts(ctx, q)
	check(err)
	categories, err := money.Categories(ctx, q)
	check(err)
	s.page(w, r, status, "money_transactions.gohtml", transactionsPage{
		Transactions: txViews(rows), Filters: f, Accounts: accountViews(accounts), Categories: categoryViews(categories),
		Kinds: pageKinds, Currencies: money.TxCurrencies,
	}, notice)
}

func (s *service) transactionsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var errs []web.FieldError
	unassigned, bad := boolParam(q, "unassigned")
	if bad != nil {
		errs = append(errs, *bad)
	}
	dismissed, bad := boolParam(q, "dismissed")
	if bad != nil {
		errs = append(errs, *bad)
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	f := filters{
		Start: web.QueryValue(q, "start"), End: web.QueryValue(q, "end"), Payee: web.QueryValue(q, "payee"),
		Category: web.QueryValue(q, "category"), Kind: web.QueryValue(q, "kind"),
		Currency: strings.ToUpper(web.QueryValue(q, "currency")), Unassigned: unassigned, Dismissed: dismissed,
	}
	if raw := py.Strip(web.QueryValue(q, "account_id")); raw != "" && py.IsDigits(raw) {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			f.AccountID = n
		}
	}
	s.showTransactions(w, r, nil, f, http.StatusOK)
}

func (s *service) txEdit(w http.ResponseWriter, r *http.Request, tool string, txID int64, apply func(ctx context.Context, q db.Querier) (*money.Change, error)) bool {
	return s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		change, err := apply(ctx, q)
		if err != nil {
			return err
		}
		return s.recorded(ctx, q, change, tool, target("transaction", txID))
	})
}

func (s *service) transactionsMerge(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	var ids []int64
	var errs []web.FieldError
	for _, raw := range form["transaction_ids"] {
		if raw == "" {
			continue
		}
		n, ok := py.LaxInt64(raw)
		if !ok {
			errs = append(errs, web.BadInt("body", "transaction_ids", raw))
			continue
		}
		ids = append(ids, n)
	}
	if len(ids) == 0 && len(errs) == 0 {
		errs = append(errs, web.Missing("body", "transaction_ids"))
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	var kept int64
	ok := s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		id, change, err := money.TransactionMerge(ctx, q, ids)
		if err != nil {
			return err
		}
		kept = id
		return s.recorded(ctx, q, change, "transaction_merge", target("transaction", kept))
	})
	if ok {
		s.showTransactions(w, r, toast("success", "Merged", "Kept transaction "+strconv.FormatInt(kept, 10)+"."), filters{}, http.StatusOK)
	}
}

func (s *service) transactionSplit(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "tx_id")
	if f.invalid(w) {
		return
	}
	var made []int64
	if s.txEdit(w, r, "transaction_split", ids["tx_id"], func(ctx context.Context, q db.Querier) (*money.Change, error) {
		m, change, err := money.TransactionSplit(ctx, q, ids["tx_id"], s.now())
		made = m
		return change, err
	}) {
		s.showTransactions(w, r, toast("success", "Split", "Now "+strconv.Itoa(len(made))+" transactions."), filters{}, http.StatusOK)
	}
}

func (s *service) transactionCategory(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "tx_id")
	category := f.optionalID("category_id")
	if f.invalid(w) {
		return
	}
	if s.txEdit(w, r, "transaction_set_category", ids["tx_id"], func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.TransactionSetCategory(ctx, q, ids["tx_id"], category)
	}) {
		s.showTransactions(w, r, toast("success", "Category set"), filters{}, http.StatusOK)
	}
}

func (s *service) transactionCurrency(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "tx_id")
	currency := f.text("currency")
	if f.invalid(w) {
		return
	}
	if s.txEdit(w, r, "transaction_set_currency", ids["tx_id"], func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.TransactionSetCurrency(ctx, q, ids["tx_id"], currency)
	}) {
		s.showTransactions(w, r, toast("success", "Currency set", "Amount resolved in the account currency."), filters{}, http.StatusOK)
	}
}

func (s *service) transactionDismiss(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "tx_id")
	reason := py.Strip(f.textOr("reason", ""))
	if reason == "" {
		reason = "dismissed"
	}
	if f.invalid(w) {
		return
	}
	if s.txEdit(w, r, "transaction_dismiss", ids["tx_id"], func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.TransactionDismiss(ctx, q, ids["tx_id"], reason)
	}) {
		s.showTransactions(w, r, toast("success", "Dismissed", "Left out of lists, spending and bills. Tick Dismissed only to restore it."), filters{}, http.StatusOK)
	}
}

func (s *service) transactionRestore(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "tx_id")
	if f.invalid(w) {
		return
	}
	if s.txEdit(w, r, "transaction_restore", ids["tx_id"], func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.TransactionDismiss(ctx, q, ids["tx_id"], "")
	}) {
		s.showTransactions(w, r, toast("success", "Restored"), filters{Dismissed: true}, http.StatusOK)
	}
}

func (s *service) transactionsDismissBefore(w http.ResponseWriter, r *http.Request) {
	f, _ := newFields(r)
	before := f.text("before")
	if f.invalid(w) {
		return
	}
	var count int
	ok := s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		n, change, err := money.TransactionsDismissBefore(ctx, q, before, "")
		if err != nil {
			return err
		}
		count = n
		return s.recorded(ctx, q, change, "transactions_dismiss_before", "before:"+before)
	})
	if ok {
		s.showAccounts(w, r, toast("success", "Dismissed "+strconv.Itoa(count)+" transactions", "Undo it from Activity."), http.StatusOK)
	}
}
