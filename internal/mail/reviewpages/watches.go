package reviewpages

import (
	"net/http"

	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type watchesPage struct {
	Packages []watches.Package `json:"packages"`
}

func (s *service) showWatches(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	packages, err := watches.ListPackages(r.Context(), s.deps.DB.Read, s.deps.Env, s.now(), true, false)
	check(err)
	s.page(w, r, http.StatusOK, "watches", "watches.gohtml", watchesPage{Packages: packages}, notice)
}

func (s *service) watchesPage(w http.ResponseWriter, r *http.Request) { s.showWatches(w, r, nil) }

func (s *service) packageDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "package_id")
	if !ok {
		return
	}
	found, err := watches.DismissPackage(r.Context(), s.deps.DB.Write, id)
	check(err)
	if !s.notFound(w, r, found, "package") {
		return
	}
	s.showWatches(w, r, toast("success", "Dismissed"))
}
