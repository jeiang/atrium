package pages

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/toolset"
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
	Runner    *loop.Runner
	Client    *anthropic.Client
	Now       func() float64
	Poll      time.Duration
	KeepAlive time.Duration
	Registry  *registry.Registry
}

type pages struct {
	deps      *web.Deps
	svc       *service.Service
	now       func() float64
	poll      time.Duration
	keepalive time.Duration
	reg       *registry.Registry
}

const (
	threadLimit  = 50
	emptyMessage = "Type a message first."
	archived     = "archived"
)

func wallClock() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		run := o.Runner
		if run == nil {
			made, err := runner.New()
			if err != nil {
				panic(err)
			}
			run = made
		}
		now := o.Now
		if now == nil {
			now = wallClock
		}
		poll := o.Poll
		if poll == 0 {
			poll = 300 * time.Millisecond
		}
		base, cancel := context.WithCancel(context.Background())
		svc := service.New(base, d.DB, d.Cfg, d.Env, run)
		svc.Client = o.Client
		svc.Now = o.Now
		shutdown := func() {
			svc.Close()
			cancel()
		}
		if d.OnClose != nil {
			d.OnClose(shutdown)
		}
		keepalive := o.KeepAlive
		if keepalive == 0 {
			keepalive = 15 * time.Second
		}
		reg := o.Registry
		if reg == nil {
			made, err := toolset.Registry()
			if err != nil {
				panic(err)
			}
			reg = made
		}
		p := &pages{deps: d, svc: svc, now: now, poll: poll, keepalive: keepalive, reg: reg}

		r.Get("/chat", p.chatIndex)
		r.Post("/chat", p.chatNew)
		r.Post("/chat/{thread_id}/pin", func(w http.ResponseWriter, r *http.Request) { p.setFlag(w, r, "pinned") })
		r.Post("/chat/{thread_id}/archive", func(w http.ResponseWriter, r *http.Request) { p.setFlag(w, r, "archived") })
		r.Get("/chat/{thread_id}", p.chatThread)
		r.Post("/chat/{thread_id}/project", p.chatProject)
		r.Post("/chat/{thread_id}/options", p.chatOptions)
		r.Post("/chat/{thread_id}/delete", p.chatDelete)
		r.Post("/chat/{thread_id}/messages", p.chatMessage)
		r.Post("/chat/{thread_id}/regenerate", p.chatRegenerate)
		r.Post("/chat/{thread_id}/stop", p.chatStop)
		r.Post("/chat/{thread_id}/branch", p.chatBranch)
		r.Get("/chat/{thread_id}/work", p.chatWork)
		r.Get("/chat/{thread_id}/events", p.chatEvents)
		r.Get("/chat/{thread_id}/runs/{run_id}", p.chatRun)
		r.Post("/chat/{thread_id}/runs/{run_id}/resume", p.chatResume)

		r.Get("/chat/{thread_id}/files/{file_id}", p.chatFile)
		r.Get("/chat/{thread_id}/files/{file_id}/download", p.chatFileDownload)
		r.Get("/chat/{thread_id}/files/{file_id}/raw", p.chatFileRaw)
		r.Get("/projects/{project_id}/files", p.projectFilesList)
		r.Post("/projects/{project_id}/files", p.projectFilesUpload)
		r.Get("/projects/{project_id}/files/{file_id}/download", p.projectFileDownload)
		r.Post("/projects/{project_id}/files/{file_id}/delete", p.projectFileDelete)

		p.projectRoutes(r)
		p.scheduledRoutes(r)
		p.settingsRoutes(r)
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func (p *pages) page(w http.ResponseWriter, r *http.Request, name string, data any) {
	f, err := p.deps.Render.Frame(r, "chat", data)
	check(err)
	check(p.deps.Render.Page(w, r, http.StatusOK, name, f))
}

func (p *pages) fragment(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	f, err := p.deps.Render.Frame(r, "", data)
	check(err)
	check(p.deps.Render.Fragment(w, r, status, name, f))
}

func (p *pages) fail(w http.ResponseWriter, r *http.Request, status int, detail string) {
	p.deps.Error(w, r, status, detail)
}

func (p *pages) failure(w http.ResponseWriter, r *http.Request, title, description string) {
	if !web.IsPartial(r) {
		p.fail(w, r, http.StatusUnprocessableEntity, description)
		return
	}
	f, err := p.deps.Render.Frame(r, "", nil)
	check(err)
	f.Toast = &tpl.Toast{Category: "error", Title: title, Description: description}
	w.Header().Set("HX-Reswap", "none")
	check(p.deps.Render.Fragment(w, r, http.StatusUnprocessableEntity, "_toast.gohtml", f))
}

func (p *pages) refused(w http.ResponseWriter, r *http.Request, err error) bool {
	var exceeded budget.BudgetExceeded
	var active service.RunActive
	var redo service.NothingToRedo
	var unknown service.UnknownThread
	switch {
	case err == nil:
		return false
	case errors.As(err, &exceeded):
		p.failure(w, r, "Budget reached", err.Error())
	case errors.As(err, &active):
		p.failure(w, r, "Still working", "Wait for the current reply to finish, then send again.")
	case errors.As(err, &redo):
		p.failure(w, r, "Nothing to redo", err.Error())
	case errors.As(err, &unknown):
		p.fail(w, r, http.StatusNotFound, "unknown thread")
	default:
		panic(err)
	}
	return true
}

func redirect(w http.ResponseWriter, r *http.Request, to string) {
	w.Header().Set("Location", to)
	w.WriteHeader(http.StatusSeeOther)
}

func hxRedirect(w http.ResponseWriter, to string) {
	w.Header().Set("HX-Redirect", to)
	w.WriteHeader(http.StatusNoContent)
}

func pathInt(r *http.Request, name string) (int64, []web.FieldError) {
	raw := web.PathParam(r, name)
	id, ok := py.LaxInt64(raw)
	if !ok {
		return 0, []web.FieldError{web.BadInt("path", name, raw)}
	}
	return id, nil
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, errs := pathInt(r, name)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return 0, false
	}
	return id, true
}

func formInt(form map[string][]string, where, name string, def int64, errs *[]web.FieldError) int64 {
	raw, ok := web.FormField(form, name)
	if !ok {
		return def
	}
	n, valid := py.LaxInt64(raw)
	if !valid {
		*errs = append(*errs, web.BadInt(where, name, raw))
	}
	return n
}
