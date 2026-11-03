package pages

import (
	"embed"
	"io/fs"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

//go:embed templates/*.gohtml
var templates embed.FS

const dateLayout = "2006-01-02"

func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

type viewData struct {
	Items    []attention.Item  `json:"items"`
	Groups   []attention.Group `json:"groups"`
	Waiting  []attention.Item  `json:"waiting"`
	Upcoming []attention.Item  `json:"upcoming"`
	Tomorrow string            `json:"tomorrow"`
}

func Module(current func() *module.Snapshot) web.Module {
	return func(r chi.Router, d *web.Deps) {
		env := d.Env
		if env == nil {
			env = config.OS
		}
		location := func() *time.Location {
			loc, err := config.Location(env)
			if err != nil {
				panic(err)
			}
			return loc
		}
		check := func(err error) {
			if err != nil {
				panic(err)
			}
		}

		r.Get("/attention", func(w http.ResponseWriter, r *http.Request) {
			snap := current()
			if snap == nil {
				d.Error(w, r, http.StatusServiceUnavailable, "modules are not ready")
				return
			}
			now, loc := time.Now(), location()
			res, err := attention.Query(r.Context(), d.DB.Read, snap.Attention(), now, loc)
			check(err)
			data := viewData{res.Items, res.Groups, res.Waiting, res.Upcoming, now.In(loc).AddDate(0, 0, 1).Format(dateLayout)}
			f, err := d.Render.Frame(r, "attention", data)
			check(err)
			if web.IsPartial(r) {
				check(d.Render.Fragment(w, r, http.StatusOK, "_attention.gohtml", f))
				return
			}
			check(d.Render.Page(w, r, http.StatusOK, "attention.gohtml", f))
		})

		r.Post("/attention/snooze", func(w http.ResponseWriter, r *http.Request) {
			form := web.FormValues(r)
			kind, _ := web.FormLast(form, "kind")
			id, _ := web.FormLast(form, "source_id")
			until, _ := web.FormLast(form, "until")
			var errs []web.FieldError
			if !slices.Contains(attention.Kinds, kind) {
				errs = append(errs, web.BadLiteral("body", "kind", kind, attention.Kinds...))
			}
			if id == "" {
				errs = append(errs, web.Missing("body", "source_id"))
			}
			when, err := time.ParseInLocation(dateLayout, until, location())
			if err != nil {
				errs = append(errs, web.BadLiteral("body", "until", until, "a date as YYYY-MM-DD"))
			}
			if len(errs) > 0 {
				web.Unprocessable(w, errs...)
				return
			}
			check(attention.Snooze(r.Context(), d.DB.Write, kind, id, when, time.Now()))
			http.Redirect(w, r, "/attention", http.StatusSeeOther)
		})
	}
}
