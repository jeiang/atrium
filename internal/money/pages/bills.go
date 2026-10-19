package pages

import (
	"net/http"

	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) showBills(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	bills, err := money.Bills(r.Context(), s.deps.DB.Read, s.today(), nil)
	check(err)
	s.page(w, r, http.StatusOK, "money_bills.gohtml", billsPage{Bills: billViews(bills)}, notice)
}

func (s *service) billsPage(w http.ResponseWriter, r *http.Request) { s.showBills(w, r, nil) }

func (s *service) billOverride(w http.ResponseWriter, r *http.Request) {
	if s.setOverride(w, r) {
		s.showBills(w, r, toast("success", "Bill override set"))
	}
}
