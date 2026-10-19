package pages

import (
	"net/http"

	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) page(w http.ResponseWriter, r *http.Request, status int, name string, data any, notice *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, "", data)
	if err != nil {
		panic(err)
	}
	f.Toast = notice
	if err := s.deps.Render.Page(w, r, status, name, f); err != nil {
		panic(err)
	}
}

func (s *service) fragment(w http.ResponseWriter, r *http.Request, name string, data any, notice *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, "", data)
	if err != nil {
		panic(err)
	}
	f.Toast = notice
	if err := s.deps.Render.Fragment(w, r, http.StatusOK, name, f); err != nil {
		panic(err)
	}
}

func (s *service) fail(w http.ResponseWriter, r *http.Request, status int, detail string) {
	s.deps.Error(w, r, status, detail)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
