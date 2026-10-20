package web

import (
	"bytes"
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type Renderer struct {
	engine *tpl.Engine
	cfg    config.Config
	db     *db.DB
}

func NewRenderer(cfg config.Config, d *db.DB, extra tpl.Config) (*Renderer, error) {
	accounts := make([]string, len(config.Accounts))
	for i, spec := range config.Accounts {
		accounts[i] = spec.Name
	}
	extra.AssetVersion = AssetVersion
	if extra.Globals == nil {
		extra.Globals = map[string]any{}
	}
	extra.Globals["accounts"] = accounts
	engine, err := tpl.New(extra)
	if err != nil {
		return nil, err
	}
	return &Renderer{engine: engine, cfg: cfg, db: d}, nil
}

func (rd *Renderer) Frame(r *http.Request, page string, data any) (*tpl.Frame, error) {
	pending, err := rd.pending(r.Context())
	if err != nil {
		return nil, err
	}
	return &tpl.Frame{
		CSRF:         CSRFToken(r.Context()),
		Mode:         rd.cfg.Mode,
		Paused:       safety.IsPaused(rd.cfg.PausePath()),
		PendingCount: pending,
		Page:         page,
		Data:         data,
	}, nil
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
	view := tpl.ErrorView{Status: status, Problem: problem, Path: r.URL.Path}
	f, err := rd.Frame(r, "", view)
	if err == nil && !IsPartial(r) {
		err = rd.Page(w, r, status, "error.gohtml", f)
	} else if err == nil {
		if status == http.StatusUnprocessableEntity {
			f.Toast = &tpl.Toast{Category: "error", Title: "Filter not understood", Description: problem}
		}
		err = rd.Fragment(w, r, status, "_error.gohtml", f)
	}
	if err != nil {
		log.Error("render error page failed", "err", err.Error())
		deny(w, http.StatusInternalServerError, "Internal Server Error")
	}
}
