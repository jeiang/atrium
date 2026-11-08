package web

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type FrameFunc func(context.Context, *tpl.Frame) error

type Renderer struct {
	oidcOn atomic.Bool
	engine *tpl.Engine
	cfg    config.Config
	db     *db.DB
	frame  []FrameFunc
	mu     sync.Mutex
}

func (rd *Renderer) OnFrame(fn FrameFunc) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.frame = append(rd.frame, fn)
}

func NewRenderer(cfg config.Config, d *db.DB, extra tpl.Config) (*Renderer, error) {
	rd := &Renderer{cfg: cfg, db: d}
	extra.AssetVersion = AssetVersion
	if extra.Globals == nil {
		extra.Globals = map[string]any{}
	}
	extra.Globals["accounts"] = func() []string {
		specs := config.Accounts()
		accounts := make([]string, len(specs))
		for i, spec := range specs {
			accounts[i] = spec.Name
		}
		return accounts
	}
	if _, ok := extra.Globals["module_enabled"]; !ok {
		extra.Globals["module_enabled"] = func(string) bool { return true }
	}
	enabled, _ := extra.Globals["module_enabled"].(func(string) bool)
	extra.Globals["module_enabled"] = func(name string) bool {
		if name == "auth" {
			return rd.oidcOn.Load()
		}
		return enabled(name)
	}
	engine, err := tpl.New(extra)
	if err != nil {
		return nil, err
	}
	rd.engine = engine
	return rd, nil
}

func (rd *Renderer) Frame(r *http.Request, page string, data any) (*tpl.Frame, error) {
	pending := 0
	if n, err := rd.pending(r.Context()); err != nil {
		log.Error("frame pending count failed", "err", err.Error())
	} else {
		pending = n
	}
	mode := config.Shadow
	if m, err := rd.mode(r.Context()); err != nil {
		log.Error("frame mail mode failed", "err", err.Error())
	} else {
		mode = m
	}
	var auth tpl.AuthView
	if who, ok := IdentityOf(r.Context()); ok {
		auth = tpl.AuthView{Admin: who.Admin, SignOut: who.Source == SourceOIDC, NoAuth: who.NoAuth}
	}
	var failing, alerts []string
	if auth.Admin {
		names, err := rd.names(r.Context(), "SELECT name FROM ntfy_channels WHERE failures >= 3 ORDER BY name")
		if err != nil {
			log.Error("frame notify state failed", "err", err.Error())
		} else {
			failing = names
		}
		names, err = rd.names(r.Context(), "SELECT name || ' ' || replace(state, '_', ' ') FROM integration_health WHERE state <> 'ok' ORDER BY name")
		if err != nil {
			log.Error("frame integration state failed", "err", err.Error())
		} else {
			alerts = names
		}
	}
	f := &tpl.Frame{
		Auth:              auth,
		CSRF:              CSRFToken(r.Context()),
		Mode:              mode,
		Paused:            safety.IsPaused(rd.cfg.PausePath()),
		PendingCount:      pending,
		NotifyFailing:     failing,
		IntegrationAlerts: alerts,
		Page:              page,
		Data:              data,
	}
	rd.mu.Lock()
	contributions := append([]FrameFunc(nil), rd.frame...)
	rd.mu.Unlock()
	for _, fn := range contributions {
		if err := fn(r.Context(), f); err != nil {
			log.Error("frame contribution failed", "err", err.Error())
		}
	}
	return f, nil
}

func (rd *Renderer) names(ctx context.Context, query string) ([]string, error) {
	if rd.db == nil {
		return nil, nil
	}
	rows, err := rd.db.Read.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var name string
		return name, r.Scan(&name)
	})
}

func (rd *Renderer) mode(ctx context.Context) (string, error) {
	if rd.db == nil {
		return config.Shadow, nil
	}
	return mail.Mode(ctx, rd.db.Read)
}

func (rd *Renderer) pending(ctx context.Context) (int, error) {
	if rd.db == nil {
		return 0, nil
	}
	var n int
	err := rd.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE status = 'pending'").Scan(&n)
	return n, err
}

func (rd *Renderer) Page(w http.ResponseWriter, r *http.Request, status int, name string, f *tpl.Frame) error {
	var b bytes.Buffer
	if err := rd.engine.Page(&b, name, f); err != nil {
		return err
	}
	return write(w, status, &b)
}

func (rd *Renderer) Fragment(w http.ResponseWriter, r *http.Request, status int, name string, f *tpl.Frame) error {
	var b bytes.Buffer
	if err := rd.engine.Fragment(&b, name, f); err != nil {
		return err
	}
	return write(w, status, &b)
}

func write(w http.ResponseWriter, status int, b *bytes.Buffer) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := w.Write(b.Bytes())
	return err
}

func (rd *Renderer) ErrorPage(w http.ResponseWriter, r *http.Request, status int, problem string) {
	view := tpl.ErrorView{Status: status, Problem: problem, Path: r.URL.Path, AdminOnly: AdminOnly(r.Context())}
	f, err := rd.Frame(r, "", view)
	if err == nil && !IsPartial(r) {
		err = rd.Page(w, r, status, "error.gohtml", f)
	} else if err == nil {
		switch {
		case status == http.StatusUnprocessableEntity:
			f.Toast = &tpl.Toast{Category: "error", Title: "Filter not understood", Description: problem}
		case view.AdminOnly:
			f.Toast = &tpl.Toast{Category: "error", Title: "Admins only"}
		}
		err = rd.Fragment(w, r, status, "_error.gohtml", f)
	}
	if err != nil {
		log.Error("render error page failed", "err", err.Error())
		deny(w, http.StatusInternalServerError, "Internal Server Error")
	}
}
