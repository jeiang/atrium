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
	Thread *int64  `json:"thread"`
	Old    *string `json:"old"`
	New    *string `json:"new"`
	Actor  string  `json:"actor"`
	Source string  `json:"source"`
}

type handlers struct{ d *web.Deps }

func Module(r chi.Router, d *web.Deps) {
	h := handlers{d}
	r.Get(path, h.first)
	r.Get(path+"/{module}", h.index)
	r.Post(path+"/{module}", h.save)
	r.Get(auditPath, h.audit)
}

func shown(d settings.Def) bool { return !d.Generated }

func modules() []string {
	var out []string
	for _, d := range settings.Default.Defs() {
		if shown(d) && !slices.Contains(out, d.Module) {
			out = append(out, d.Module)
		}
	}
	slices.Sort(out)
	return out
}

func (h handlers) first(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", path+"/"+modules()[0])
	w.WriteHeader(http.StatusSeeOther)
}

func fields(ctx context.Context, q db.Querier, module string) ([]field, error) {
	var out []field
	store := secrets.NewIn(nil, q, nil)
	for _, d := range settings.Default.Defs() {
		if d.Module != module || !shown(d) {
			continue
		}
		f := field{Key: d.Key, Type: string(d.Type), Choices: d.Choices, Default: d.Default, Secret: d.Secret, Restart: d.Restart, Page: d.Page}
		if source, pinned, err := settings.Default.Pinned(d.Key); err != nil {
			return nil, err
		} else if pinned {
			f.Pinned = source
		}
		if d.Secret {
			status, err := store.IsSet(ctx, d.Key)
			if err != nil {
				return nil, err
			}
			f.SecretSet = status == secrets.Set
			f.Editable = f.Pinned == "" && !d.PinOnly && d.Page == ""
			out = append(out, f)
			continue
		}
		found, err := settings.Default.Resolve(ctx, q, d.Key, 0)
		if err != nil {
			return nil, err
		}
		f.Value, f.Stored = found.Value, found.Origin == settings.FromInstance
		f.Editable = f.Pinned == "" && slices.Contains(d.Scopes, settings.Instance) && d.Page == ""
		out = append(out, f)
	}
	return out, nil
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data page) {
	if !slices.Contains(modules(), data.Module) {
		http.NotFound(w, r)
		return
	}
	list, err := fields(r.Context(), h.d.DB.Read, data.Module)
	if err != nil {
		panic(err)
	}
	for i := range list {
		if list[i].Key == data.Failed && !list[i].Secret && len(data.Entered) > 0 {
			list[i].Value = web.LastValue(data.Entered, "")
		}
	}
	data.Modules, data.Fields = modules(), list
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
	if !slices.Contains(modules(), module) {
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
		store := secrets.NewIn(d, tx, d)
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
			return settings.Default.Unset(ctx, tx, f.Key, 0)
		}
		return nil
	}
	value, ok := form["v."+f.Key]
	if !ok || web.LastValue(value, "") == f.Value {
		return nil
	}
	return settings.Default.Set(ctx, tx, f.Key, web.LastValue(value, ""), 0)
}

func refused(err error) bool {
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var scope *settings.ScopeError
	var encryption secrets.EncryptionRequired
	var empty secrets.EmptyValue
	return errors.As(err, &invalid) || errors.As(err, &pinned) || errors.As(err, &scope) || errors.As(err, &encryption) || errors.As(err, &empty)
}

func (h handlers) audit(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.DB.Read.QueryContext(r.Context(),
		"SELECT ts, key, thread_id, old_value, new_value, actor, source FROM settings_audit ORDER BY id DESC LIMIT ?", auditRows)
	if err != nil {
		panic(err)
	}
	list, err := db.Collect(rows, func(rs *sql.Rows) (entry, error) {
		var e entry
		return e, rs.Scan(&e.TS, &e.Key, &e.Thread, &e.Old, &e.New, &e.Actor, &e.Source)
	})
	if err != nil {
		panic(err)
	}
	h.page(w, r, http.StatusOK, nil, "settings_audit.gohtml", map[string]any{"entries": list})
}
