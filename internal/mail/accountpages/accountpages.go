package accountpages

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/mail/imapclient"
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

const path = "/settings/mail"

type refusal string

func (e refusal) Error() string { return string(e) }

type account struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Preset      string `json:"preset"`
	Login       string `json:"login"`
	Enabled     bool   `json:"enabled"`
	Pinned      bool   `json:"pinned"`
	PasswordSet bool   `json:"password_set"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
}

type page struct {
	Accounts []account         `json:"accounts"`
	Presets  []string          `json:"presets"`
	Failed   string            `json:"failed"`
	Error    string            `json:"error"`
	Form     map[string]string `json:"form"`
}

type handlers struct{ d *web.Deps }

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func healthName(id string) string { return "mail_" + id }

func Module(r chi.Router, d *web.Deps) {
	h := handlers{d}
	r.Get(path, h.index)
	r.Post(path+"/accounts", h.add)
	r.Post(path+"/accounts/{id}", h.update)
	r.Post(path+"/accounts/{id}/test", h.test)
	r.Post(path+"/accounts/{id}/remove", h.remove)
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func (h handlers) accounts(ctx context.Context) ([]account, error) {
	var out []account
	for _, a := range config.Accounts() {
		v := account{ID: a.Name, Label: a.Label, Preset: a.Preset, Login: a.Login, Enabled: a.Enabled, Pinned: !a.Stored, PasswordSet: a.Password != "", State: integrations.Untested}
		err := h.d.DB.Read.QueryRowContext(ctx, "SELECT state, reason FROM integration_health WHERE name = ?", healthName(a.Name)).Scan(&v.State, &v.Reason)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data page) {
	list, err := h.accounts(r.Context())
	if err != nil {
		panic(err)
	}
	data.Accounts, data.Presets = list, []string{config.ICloud, config.Gmail}
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "mail_accounts.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, nil, page{})
}

func (h handlers) refuse(w http.ResponseWriter, r *http.Request, failed string, form map[string]string, err error) bool {
	var why refusal
	var invalid settings.Invalid
	var encryption secrets.EncryptionRequired
	if !errors.As(err, &why) && !errors.As(err, &invalid) && !errors.As(err, &encryption) {
		return false
	}
	h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, page{Failed: failed, Error: err.Error(), Form: form})
	return true
}

func (h handlers) fail(w http.ResponseWriter, r *http.Request, failed string, form map[string]string, err error) {
	if err != nil && h.refuse(w, r, failed, form, err) {
		return
	}
	if err != nil {
		panic(err)
	}
}

func (h handlers) stored(id string) (config.AccountSpec, error) {
	a, ok := config.Account(id)
	switch {
	case !ok:
		return a, errNotFound
	case !a.Stored:
		return a, refusal(id + " is set by deployment")
	}
	return a, nil
}

var errNotFound = errors.New("not found")

func probe(ctx context.Context, a config.AccountSpec) error {
	if err := imapclient.Login(ctx, a); err != nil {
		return refusal("Test failed: " + err.Error())
	}
	return nil
}

func (h handlers) add(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	field := func(name string) string { return strings.TrimSpace(web.FormString(form, name, "")) }
	kept := map[string]string{"id": field("id"), "label": field("label"), "preset": field("preset"), "login": field("login")}
	password := web.FormString(form, "password", "")
	ctx := h.ctx(r)
	a, err := config.NewAccount(kept["id"], kept["label"], kept["preset"], kept["login"], password, true)
	switch {
	case err != nil:
		err = refusal(err.Error())
	case a.Login == "" || a.Password == "":
		err = refusal("login and password are required")
	case !h.d.DB.Encrypted():
		err = secrets.EncryptionRequired(config.PasswordKey(a.Name))
	default:
		if _, exists := config.Account(a.Name); exists {
			err = refusal(a.Name + " is already an account id")
		}
	}
	if err == nil {
		err = probe(ctx, a)
	}
	if err == nil {
		err = h.save(ctx, a, true, true)
	}
	if err != nil {
		h.fail(w, r, "new", kept, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account added", Description: "Restart the atrium units to sync it."}, page{})
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := h.stored(id)
	if errors.Is(err, errNotFound) {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	ctx := h.ctx(r)
	var a config.AccountSpec
	if err == nil {
		password := web.FormString(form, "password", "")
		login := strings.TrimSpace(web.FormString(form, "login", current.Login))
		next := current.Password
		if password != "" {
			next = password
		}
		a, err = config.NewAccount(id, strings.TrimSpace(web.FormString(form, "label", current.Label)), current.Preset, login, next, web.FormString(form, "enabled", "") != "")
		if err == nil && a.Login == "" {
			err = refusal("login is required")
		}
		tested := a.Login != current.Login || password != ""
		if err == nil && tested {
			err = probe(ctx, a)
		}
		if err == nil {
			err = h.save(ctx, a, password != "", tested)
		}
	}
	if err != nil {
		h.fail(w, r, id, nil, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account saved", Description: "Restart the atrium units to apply it."}, page{})
}

func (h handlers) save(ctx context.Context, a config.AccountSpec, password, tested bool) error {
	if err := config.RegisterPassword(a.Name); err != nil {
		return err
	}
	err := h.d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO mail_accounts (id, label, preset, login, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET label = excluded.label, login = excluded.login, enabled = excluded.enabled",
			a.Name, a.Label, a.Preset, a.Login, a.Enabled, stamp()); err != nil {
			return err
		}
		if password {
			return secrets.NewIn(h.d.DB, tx, h.d.DB).Set(ctx, config.PasswordKey(a.Name), a.Password, "ui")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := config.LoadStoredAccounts(ctx, h.d.DB.Read); err != nil {
		return err
	}
	if tested {
		return integrations.RecordResult(ctx, h.d.DB, h.d.Env, healthName(a.Name), a.Label, integrations.OK, "", stamp())
	}
	return nil
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	a, ok := config.Account(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := h.ctx(r)
	state, reason := integrations.OK, ""
	if err := imapclient.Login(ctx, a); err != nil {
		state, reason = integrations.Failing, err.Error()
	}
	if err := integrations.RecordResult(ctx, h.d.DB, h.d.Env, healthName(id), a.Label, state, reason, stamp()); err != nil {
		panic(err)
	}
	if state != integrations.OK {
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Test failed", Description: reason}, page{Failed: id, Error: reason})
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connection works"}, page{})
}

func (h handlers) remove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, err := h.stored(id)
	if errors.Is(err, errNotFound) {
		http.NotFound(w, r)
		return
	}
	ctx := h.ctx(r)
	if err == nil {
		err = h.d.DB.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM mail_accounts WHERE id = ?", id); err != nil {
				return err
			}
			if err := secrets.NewIn(h.d.DB, tx, h.d.DB).Delete(ctx, config.PasswordKey(id)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM integration_health WHERE name = ?", healthName(id))
			return err
		})
	}
	if err == nil {
		err = config.LoadStoredAccounts(ctx, h.d.DB.Read)
	}
	if err != nil {
		h.fail(w, r, id, nil, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account removed", Description: "Its cached mail stays."}, page{})
}
