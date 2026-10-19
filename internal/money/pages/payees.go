package pages

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) showPayees(w http.ResponseWriter, r *http.Request, notice *tpl.Toast, query string) {
	ctx := r.Context()
	q := s.deps.DB.Read
	payees, err := money.ListPayees(ctx, q, query, 200)
	check(err)
	categories, err := money.Categories(ctx, q)
	check(err)
	s.page(w, r, http.StatusOK, "money_payees.gohtml", payeesPage{
		Payees: payeeViews(payees), Query: query, Categories: categoryViews(categories), AliasKinds: money.AliasKinds,
	}, notice)
}

func (s *service) payeesPage(w http.ResponseWriter, r *http.Request) {
	s.showPayees(w, r, nil, web.QueryValue(r.URL.Query(), "q"))
}

func (s *service) payeeEdit(w http.ResponseWriter, r *http.Request, tool, target string, apply func(ctx context.Context, q db.Querier) (*money.Change, error)) bool {
	return s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		change, err := apply(ctx, q)
		if err != nil {
			return err
		}
		return s.recorded(ctx, q, change, tool, target)
	})
}

func (s *service) payeeRename(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "payee_id")
	name := f.text("name")
	if f.invalid(w) {
		return
	}
	id := ids["payee_id"]
	if s.payeeEdit(w, r, "payee_rename", target("payee", id), func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.PayeeRename(ctx, q, id, name)
	}) {
		s.showPayees(w, r, toast("success", "Renamed"), "")
	}
}

func (s *service) payeeCategory(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "payee_id")
	category := f.optionalID("category_id")
	if f.invalid(w) {
		return
	}
	id := ids["payee_id"]
	if s.payeeEdit(w, r, "payee_set_category", target("payee", id), func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.PayeeSetCategory(ctx, q, id, category)
	}) {
		s.showPayees(w, r, toast("success", "Category set"), "")
	}
}

func (s *service) setOverride(w http.ResponseWriter, r *http.Request) bool {
	f, ids := newFields(r, "payee_id")
	override := f.textOr("override", "")
	if f.invalid(w) {
		return false
	}
	var value *string
	if override != "" {
		value = &override
	}
	id := ids["payee_id"]
	return s.payeeEdit(w, r, "payee_set_override", target("payee", id), func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.PayeeSetOverride(ctx, q, id, value)
	})
}

func (s *service) payeeOverride(w http.ResponseWriter, r *http.Request) {
	if s.setOverride(w, r) {
		s.showPayees(w, r, toast("success", "Bill override set"), "")
	}
}

func (s *service) payeeMerge(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "payee_id")
	targetID := f.integer("target_id")
	if f.invalid(w) {
		return
	}
	id := ids["payee_id"]
	if s.payeeEdit(w, r, "payee_merge", target("payee", targetID), func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.PayeeMerge(ctx, q, id, targetID)
	}) {
		s.showPayees(w, r, toast("success", "Merged", "Aliases and transactions moved."), "")
	}
}

func (s *service) aliasAdd(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "payee_id")
	pattern := f.text("pattern")
	kind := f.textOr("kind", money.Exact)
	if f.invalid(w) {
		return
	}
	id := ids["payee_id"]
	if s.payeeEdit(w, r, "payee_alias_add", target("payee", id), func(ctx context.Context, q db.Querier) (*money.Change, error) {
		_, change, err := money.PayeeAliasAdd(ctx, q, id, pattern, kind)
		return change, err
	}) {
		s.showPayees(w, r, toast("success", "Alias added"), "")
	}
}

func (s *service) aliasDelete(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "alias_id")
	if f.invalid(w) {
		return
	}
	id := ids["alias_id"]
	if s.payeeEdit(w, r, "payee_alias_delete", target("alias", id), func(ctx context.Context, q db.Querier) (*money.Change, error) {
		return money.PayeeAliasDelete(ctx, q, id)
	}) {
		s.showPayees(w, r, toast("success", "Alias removed"), "")
	}
}

func (s *service) categoryAdd(w http.ResponseWriter, r *http.Request) {
	f, _ := newFields(r)
	name := f.text("name")
	if f.invalid(w) {
		return
	}
	if s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		_, err := money.AddCategory(ctx, q, name)
		return err
	}) {
		s.showPayees(w, r, toast("success", "Category added"), "")
	}
}

func (s *service) categoryRename(w http.ResponseWriter, r *http.Request) {
	f, ids := newFields(r, "cat_id")
	name := f.text("name")
	if f.invalid(w) {
		return
	}
	var found bool
	if !s.edit(w, r, func(ctx context.Context, q db.Querier) error {
		ok, err := money.RenameCategory(ctx, q, ids["cat_id"], name)
		found = ok
		return err
	}) {
		return
	}
	if !found {
		s.fail(w, r, http.StatusNotFound, "unknown category")
		return
	}
	s.showPayees(w, r, toast("success", "Category renamed"), "")
}
