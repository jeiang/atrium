package reviewpages

import (
	"net/http"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type watchesPage struct {
	Packages []mailapp.Package `json:"packages"`
}

func (s *service) showWatches(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	packages, err := s.mail.Packages(r.Context(), mailapp.PackagesInput{ActiveOnly: true})
	check(err)
	s.page(w, r, http.StatusOK, "watches", "watches.gohtml", watchesPage{Packages: packages.Data.Packages}, notice)
}

func (s *service) watchesPage(w http.ResponseWriter, r *http.Request) { s.showWatches(w, r, nil) }

func (s *service) packageDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "package_id")
	if !ok {
		return
	}
	_, err := s.mail.PackageDismiss(r.Context(), mailapp.PackageInput{ID: id})
	if !s.changed(w, r, err) {
		return
	}
	s.showWatches(w, r, toast("success", "Dismissed"))
}
