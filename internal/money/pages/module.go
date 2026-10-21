package pages

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

//go:embed templates/*.gohtml
var templates embed.FS

func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

type Options struct {
	Now   func() float64
	Fetch money.Fetch
}

type service struct {
	deps  *web.Deps
	now   func() float64
	fetch money.Fetch
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		now := o.Now
		if now == nil {
			now = wallClock
		}
		s := &service{deps: d, now: now, fetch: o.Fetch}

		r.Get("/money", s.overviewPage)
		r.Post("/money/transactions/{tx_id}/account", s.transactionAccount)

		r.Get("/money/transactions", s.transactionsPage)
		r.Post("/money/transactions/merge", s.transactionsMerge)
		r.Post("/money/transactions/{tx_id}/split", s.transactionSplit)
		r.Post("/money/transactions/{tx_id}/category", s.transactionCategory)
		r.Post("/money/transactions/{tx_id}/currency", s.transactionCurrency)
		r.Post("/money/transactions/{tx_id}/dismiss", s.transactionDismiss)
		r.Post("/money/transactions/{tx_id}/restore", s.transactionRestore)
		r.Post("/money/transactions/dismiss-before", s.transactionsDismissBefore)

		r.Get("/money/payees", s.payeesPage)
		r.Post("/money/payees/{payee_id}/rename", s.payeeRename)
		r.Post("/money/payees/{payee_id}/category", s.payeeCategory)
		r.Post("/money/payees/{payee_id}/override", s.payeeOverride)
		r.Post("/money/payees/{payee_id}/merge", s.payeeMerge)
		r.Post("/money/payees/{payee_id}/aliases", s.aliasAdd)
		r.Post("/money/aliases/{alias_id}/delete", s.aliasDelete)
		r.Post("/money/categories", s.categoryAdd)
		r.Post("/money/categories/{cat_id}/rename", s.categoryRename)

		r.Get("/money/accounts", s.accountsPage)
		r.Post("/money/accounts", s.accountAdd)
		r.Post("/money/accounts/{account_id}", s.accountUpdate)
		r.Post("/money/since", s.trackingSince)
		r.Post("/money/accounts/{account_id}/delete", s.accountDelete)
		r.Post("/money/rates/fetch", s.ratesFetch)

		r.Get("/money/bills", s.billsPage)
		r.Post("/money/bills/{payee_id}/override", s.billOverride)
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func toast(category, title string, description ...string) *tpl.Toast {
	t := &tpl.Toast{Category: category, Title: title}
	if len(description) > 0 {
		t.Description = description[0]
	}
	return t
}

func (s *service) page(w http.ResponseWriter, r *http.Request, status int, name string, data any, notice *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, "money", data)
	check(err)
	f.Toast = notice
	check(s.deps.Render.Page(w, r, status, name, f))
}

func (s *service) fail(w http.ResponseWriter, r *http.Request, status int, detail string) {
	s.deps.Error(w, r, status, detail)
}

func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

func (s *service) today() time.Time {
	loc, err := config.Location(s.deps.Env)
	check(err)
	return money.LocalDate(s.now(), loc)
}

func (s *service) moneyContext() money.Context { return money.Context{Now: s.now} }

func (s *service) edit(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, q db.Querier) error) bool {
	ctx := detached(r)
	err := s.deps.DB.WithTx(ctx, func(tx *sql.Tx) error { return fn(ctx, tx) })
	if err == nil {
		return true
	}
	var ledger money.LedgerError
	if errors.As(err, &ledger) {
		s.fail(w, r, http.StatusUnprocessableEntity, ledger.Error())
		return false
	}
	panic(err)
}

func (s *service) recorded(ctx context.Context, q db.Querier, change *money.Change, tool, target string) error {
	_, err := change.Record(ctx, q, s.moneyContext(), tool, target)
	return err
}

type fields struct {
	form url.Values
	errs []web.FieldError
}

func newFields(r *http.Request, pathNames ...string) (*fields, map[string]int64) {
	f := &fields{}
	ids := map[string]int64{}
	for _, name := range pathNames {
		raw := web.PathParam(r, name)
		id, ok := py.LaxInt64(raw)
		if !ok {
			f.errs = append(f.errs, web.BadInt("path", name, raw))
		}
		ids[name] = id
	}
	f.form = web.FormValues(r)
	return f, ids
}

func (f *fields) text(name string) string {
	v, ok := web.FormField(f.form, name)
	if !ok {
		f.errs = append(f.errs, web.Missing("body", name))
	}
	return v
}

func (f *fields) textOr(name, def string) string { return web.FormString(f.form, name, def) }

func (f *fields) integer(name string) int64 {
	v, ok := web.FormField(f.form, name)
	if !ok {
		f.errs = append(f.errs, web.Missing("body", name))
		return 0
	}
	n, ok := py.LaxInt64(v)
	if !ok {
		f.errs = append(f.errs, web.BadInt("body", name, v))
	}
	return n
}

func (f *fields) optionalID(name string) *int64 {
	v, ok := web.FormField(f.form, name)
	if !ok {
		return nil
	}
	n, ok := py.LaxInt64(v)
	if !ok {
		f.errs = append(f.errs, web.BadInt("body", name, v))
		return nil
	}
	if n == 0 {
		return nil
	}
	return &n
}

func (f *fields) invalid(w http.ResponseWriter) bool {
	if len(f.errs) == 0 {
		return false
	}
	web.Unprocessable(w, f.errs...)
	return true
}

func boolParam(q url.Values, name string) (bool, *web.FieldError) {
	values, sent := q[name]
	if !sent {
		return false, nil
	}
	raw := values[len(values)-1]
	switch strings.ToLower(raw) {
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	}
	return false, &web.FieldError{
		Type: "bool_parsing", Loc: []string{"query", name}, Input: raw,
		Msg: "Input should be a valid boolean, unable to interpret input",
	}
}

func target(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }
