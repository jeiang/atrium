package pages

import (
	"net/http"

	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func notice(category, title, description string) *tpl.Toast {
	return &tpl.Toast{Category: category, Title: title, Description: description}
}

func (p *pages) render(w http.ResponseWriter, r *http.Request, status int, frame, name string, data any, toast *tpl.Toast) {
	f, err := p.deps.Render.Frame(r, frame, data)
	check(err)
	f.Toast = toast
	check(p.deps.Render.Page(w, r, status, name, f))
}

func (p *pages) renderFragment(w http.ResponseWriter, r *http.Request, status int, name string, data any, toast *tpl.Toast) {
	f, err := p.deps.Render.Frame(r, "", data)
	check(err)
	f.Toast = toast
	check(p.deps.Render.Fragment(w, r, status, name, f))
}

func navigate(w http.ResponseWriter, r *http.Request, to string) {
	if web.IsPartial(r) {
		hxRedirect(w, to)
		return
	}
	redirect(w, r, to)
}
