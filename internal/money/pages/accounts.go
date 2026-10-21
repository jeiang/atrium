package pages

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const ratesShown = 8

func (s *service) showAccounts(w http.ResponseWriter, r *http.Request, notice *tpl.Toast, status int) {
	ctx := r.Context()
	q := s.deps.DB.Read
	accounts, err := money.ListAccounts(ctx, q)
	check(err)
	rates, err := money.ListRates(ctx, q, ratesShown)
	check(err)
	since, err := money.TrackedSince(ctx, q)
	check(err)
	var sincePtr *string
	if since != "" {
		sincePtr = &since
	}
	loaded, err := money.LoadBanks(ctx, q)
	check(err)
	currencies, _, err := money.LoadCurrencies(ctx, q)
	check(err)
	banks, senders := bankDicts(loaded)
	s.page(w, r, status, "money_accounts.gohtml", accountsPage{
		Accounts: accountViews(accounts), Banks: banks, Kinds: money.AccountKinds, Currencies: currencies,
		DefaultSenders: senders, Rates: rateViews(rates), Since: sincePtr,
	}, notice)
}

func (s *service) accountsPage(w http.ResponseWriter, r *http.Request) {
	s.showAccounts(w, r, nil, http.StatusOK)
}

func (s *service) accountAdd(w http.ResponseWriter, r *http.Request) {
	f, _ := newFields(r)
	bank := f.text("bank")
	name := f.text("name")
	kind := f.text("kind")
	currency := f.text("currency")
	last4 := f.textOr("last4", "")
	senders := f.textOr("sender_patterns", "")
	forex := f.textOr("forex", "") != ""
	if f.invalid(w) {
		return
	}
	in := money.NewAccount{Bank: bank, Name: name, Kind: kind, Currency: currency, Last4: last4, Forex: forex}
	if senders != "" {
		parts, err := money.SplitSenders(senders)
		check(err)
		in.SenderPatterns = parts
	}
	if s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		_, err := money.AddAccount(ctx, q, in, s.now())
		return err
	}) {
		s.showAccounts(w, r, toast("success", "Account added"), http.StatusOK)
	}
}

func (s *service) accountUpdate(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "account_id")
	name := f.text("name")
	last4 := f.textOr("last4", "")
	senders := f.textOr("sender_patterns", "")
	forex := f.textOr("forex", "") != ""
	if f.invalid(w) {
		return
	}
	parts, err := money.SplitSenders(senders)
	check(err)
	var found bool
	if !s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		ok, err := money.UpdateAccount(ctx, q, ids["account_id"], money.AccountUpdate{Name: &name, Last4: &last4, SenderPatterns: &parts, Forex: &forex})
		found = ok
		return err
	}) {
		return
	}
	if !found {
		s.fail(w, r, http.StatusNotFound, "unknown account")
		return
	}
	s.showAccounts(w, r, toast("success", "Account updated"), http.StatusOK)
}

func (s *service) trackingSince(w http.ResponseWriter, r *http.Request) {
	f, _ := newFields(r)
	since := f.textOr("since", "")
	if f.invalid(w) {
		return
	}
	var value string
	if !s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		v, err := money.SetTrackedSince(ctx, q, since)
		value = v
		return err
	}) {
		return
	}
	description := "All history is tracked."
	if value != "" {
		description = "Spending before " + value + " counts only bill charges."
	}
	s.showAccounts(w, r, toast("success", "Tracking start saved", description), http.StatusOK)
}

func (s *service) accountDelete(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "account_id")
	if f.invalid(w) {
		return
	}
	var found bool
	if !s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		ok, err := money.DeleteAccount(ctx, q, ids["account_id"])
		found = ok
		return err
	}) {
		return
	}
	if !found {
		s.fail(w, r, http.StatusNotFound, "unknown account")
		return
	}
	s.showAccounts(w, r, toast("success", "Account removed"), http.StatusOK)
}

func (s *service) ratesFetch(w http.ResponseWriter, r *http.Request) {
	loc, err := config.Location(s.deps.Env)
	check(err)
	result, err := money.FetchRates(detached(r), s.deps.DB.Write, s.now(), loc, s.fetch)
	if err != nil {
		var unavailable *httpx.ServiceUnavailable
		if errors.As(err, &unavailable) || errors.Is(err, httpx.ErrValue) {
			s.showAccounts(w, r, toast("error", "Rate fetch failed", httpx.Message(err)), http.StatusBadGateway)
			return
		}
		panic(err)
	}
	asOf := result.AsOf
	if asOf == "" {
		asOf = result.Date
	}
	s.showAccounts(w, r, toast("success", "Rate stored", "USD selling "+py.FloatRepr(result.Selling)+" as of "+asOf+"."), http.StatusOK)
}
