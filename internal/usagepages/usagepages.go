package usagepages

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const URL = "/settings/usage"

//go:embed templates/*.gohtml
var assets embed.FS

func Templates() fs.FS {
	files, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err)
	}
	return files
}

type Data struct {
	Budget       inference.Status      `json:"budget"`
	Summary      inference.Summary     `json:"summary"`
	Memory       inference.Summary     `json:"memory"`
	Subscription inference.Summary     `json:"subscription"`
	ByKind       []inference.KindRow   `json:"by_kind"`
	ByThread     []inference.ThreadRow `json:"by_thread"`
}

func Module() web.Module {
	return func(_ chi.Router, d *web.Deps) {
		page := func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame)) {
			f, err := d.Render.Frame(r, "settings", load(r, d, assistant.Now()))
			if err != nil {
				panic(err)
			}
			if mutate != nil {
				mutate(f)
			}
			if err := d.Render.Page(w, r, status, "usage.gohtml", f); err != nil {
				panic(err)
			}
		}
		d.Authenticated(http.MethodGet, URL, func(w http.ResponseWriter, r *http.Request) { page(w, r, http.StatusOK, nil) })
		d.SettingsAt(URL, page)
	}
}

func load(r *http.Request, d *web.Deps, now float64) Data {
	ctx := r.Context()
	loc, err := config.From(ctx).Location(d.Env)
	must(err)
	var out Data
	out.Budget, err = inference.StatusAt(ctx, d.DB.Read, now, loc)
	must(err)
	out.Summary, err = inference.MonthSummary(ctx, d.DB.Read, now, loc)
	must(err)
	out.ByKind, err = inference.ByKind(ctx, d.DB.Read, now, loc)
	must(err)
	out.ByThread, err = inference.ByThread(ctx, d.DB.Read, now, loc, inference.DefaultThreadLimit)
	must(err)
	ids := make([]int64, len(out.ByThread))
	for i := range out.ByThread {
		ids[i] = out.ByThread[i].ThreadID
	}
	titles, err := threads.Titles(ctx, d.DB.Read, ids)
	must(err)
	kinds, err := threads.KindsOf(ctx, d.DB.Read, ids)
	must(err)
	for i := range out.ByThread {
		if title, ok := titles[out.ByThread[i].ThreadID]; ok {
			out.ByThread[i].Title = &title
		}
		out.ByThread[i].Kind = string(kinds[out.ByThread[i].ThreadID])
	}
	out.Memory, err = inference.MemorySummary(ctx, d.DB.Read, now, loc)
	must(err)
	out.Subscription, err = inference.SubscriptionSummary(ctx, d.DB.Read, now, loc)
	must(err)
	return out
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
