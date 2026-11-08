package settingspages

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
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

const (
	path      = "/settings/config"
	auditPath = "/settings/audit"
	auditRows = 200
)

type field struct {
	Key       string   `json:"key"`
	Type      string   `json:"type"`
	Choices   []string `json:"choices"`
	Value     string   `json:"value"`
	Default   string   `json:"default"`
	Stored    bool     `json:"stored"`
	Pinned    string   `json:"pinned"`
	Secret    bool     `json:"secret"`
	SecretSet bool     `json:"secret_set"`
	Editable  bool     `json:"editable"`
	Restart   bool     `json:"restart"`
	Page      string   `json:"page"`
}

type page struct {
	Modules []string `json:"modules"`
	Module  string   `json:"module"`
	Fields  []field  `json:"fields"`
	Failed  string   `json:"failed"`
	Entered []string `json:"-"`
	Error   string   `json:"error"`
}

type entry struct {
	TS     float64 `json:"ts"`
	Key    string  `json:"key"`
	Old    *string `json:"old"`
	New    *string `json:"new"`
	Actor  string  `json:"actor"`
	Source string  `json:"source"`
}

type handlers struct{ d *web.Deps }

func Module(r chi.Router, d *web.Deps) {
	h := handlers{d}
	d.Admin(http.MethodGet, path, h.first)
	d.Admin(http.MethodGet, path+"/{module}", h.index)
	d.Admin(http.MethodPost, path+"/{module}", h.save)
	d.Admin(http.MethodGet, auditPath, h.audit)
}

func shown(d settings.Def) bool { return !d.Generated }

func modules(ctx context.Context) []string {
	var out []string
	for _, d := range settings.From(ctx).Defs() {
		if shown(d) && !slices.Contains(out, d.Module) {
			out = append(out, d.Module)
		}
	}
	slices.Sort(out)
	return out
}

func (h handlers) first(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", path+"/"+modules(r.Context())[0])
	w.WriteHeader(http.StatusSeeOther)
}

func fields(ctx context.Context, q db.Querier, module string) ([]field, error) {
	var out []field
	store := secrets.NewIn(settings.From(ctx), nil, q, nil)
	for _, d := range settings.From(ctx).Defs() {
		if d.Module != module || !shown(d) {
			continue
		}
		f := field{Key: d.Key, Type: string(d.Type), Choices: d.Choices, Default: d.Default, Secret: d.IsSecret(), Page: d.Page}
		if source, pinned, err := settings.From(ctx).Pinned(d.Key); err != nil {
			return nil, err
		} else if pinned {
			f.Pinned = source
		}
		if d.IsSecret() {
			status, err := store.Status(ctx, d.Key)
			if err != nil {
				return nil, err
			}
			f.SecretSet = status == settings.Set
			f.Editable = f.Pinned == "" && !d.PinOnly && d.Page == ""
			out = append(out, f)
			continue
		}
		found, err := settings.From(ctx).Resolve(ctx, q, d.Key)
		if err != nil {
			return nil, err
		}
		f.Value, f.Stored = found.Value, found.Origin == settings.FromInstance
		f.Editable = f.Pinned == "" && d.Editable() && d.Page == ""
		out = append(out, f)
	}
	return out, nil
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data page) {
	if !slices.Contains(modules(r.Context()), data.Module) {
		http.NotFound(w, r)
		return
	}
	list, err := fields(r.Context(), h.d.DB.Read, data.Module)
	if err != nil {
		panic(err)
	}
	for i := range list {
		if list[i].Key == data.Failed && !list[i].Secret && len(data.Entered) > 0 {
			list[i].Value = data.Entered[len(data.Entered)-1]
		}
	}
	data.Modules, data.Fields = modules(r.Context()), list
	h.page(w, r, status, toast, "settings_config.gohtml", data)
}

func (h handlers) page(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, name string, data any) {
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, name, f); err != nil {
		panic(err)
	}
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	var toast *tpl.Toast
	if web.QueryValue(r.URL.Query(), "saved") == "1" {
		toast = &tpl.Toast{Category: "success", Title: "Saved"}
	}
	h.render(w, r, http.StatusOK, toast, page{Module: chi.URLParam(r, "module")})
}

type keyError struct {
	key string
	err error
}

func (e *keyError) Error() string { return e.err.Error() }

func (e *keyError) Unwrap() error { return e.err }

func (h handlers) save(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	if !slices.Contains(modules(r.Context()), module) {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	err := h.d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		list, err := fields(ctx, tx, module)
		if err != nil {
			return err
		}
		for _, f := range list {
			if f.Editable {
				if err := apply(ctx, h.d.DB, tx, f, form); err != nil {
					return &keyError{f.Key, err}
				}
			}
		}
		return nil
	})
	var failed *keyError
	if errors.As(err, &failed) && refused(err) {
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, page{Module: module, Failed: failed.key, Entered: form["v."+failed.key], Error: err.Error()})
		return
	}
	if err != nil {
		panic(err)
	}
	w.Header().Set("Location", path+"/"+module+"?saved=1")
	w.WriteHeader(http.StatusSeeOther)
}

func apply(ctx context.Context, d *db.DB, tx *sql.Tx, f field, form url.Values) error {
	reset := slices.Contains(form["reset"], f.Key)
	if f.Secret {
		store := secrets.NewIn(settings.From(ctx), d, tx, d)
		if value := web.FormString(form, "v."+f.Key, ""); value != "" {
			return store.Set(ctx, f.Key, value, "ui")
		}
		if reset && f.SecretSet {
			return store.Delete(ctx, f.Key)
		}
		return nil
	}
	if reset {
		if f.Stored {
			return settings.From(ctx).Unset(ctx, tx, f.Key)
		}
		return nil
	}
	value, ok := web.Value(form, "v."+f.Key)
	if !ok || value == f.Value {
		return nil
	}
	return settings.From(ctx).Set(ctx, tx, f.Key, value)
}

func refused(err error) bool {
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var scope *settings.NotEditable
	var encryption settings.EncryptionRequired
	var empty settings.EmptyValue
	return errors.As(err, &invalid) || errors.As(err, &pinned) || errors.As(err, &scope) || errors.As(err, &encryption) || errors.As(err, &empty)
}

func (h handlers) audit(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.DB.Read.QueryContext(r.Context(),
		"SELECT ts, key, old_value, new_value, actor, source FROM settings_audit ORDER BY id DESC LIMIT ?", auditRows)
	if err != nil {
		panic(err)
	}
	list, err := db.Collect(rows, func(rs *sql.Rows) (entry, error) {
		var e entry
		return e, rs.Scan(&e.TS, &e.Key, &e.Old, &e.New, &e.Actor, &e.Source)
	})
	if err != nil {
		panic(err)
	}
	h.page(w, r, http.StatusOK, nil, "settings_audit.gohtml", struct {
		Entries []entry `json:"entries"`
	}{Entries: list})
}
