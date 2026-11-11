package accountpages

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	vecindex "github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailconn"
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
	AuthMethod  string `json:"auth_method"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
}

type page struct {
	Accounts []account            `json:"accounts"`
	Presets  []string             `json:"presets"`
	Indexes  []vecindex.Index     `json:"indexes"`
	Fields   []settings.FieldView `json:"fields"`
	Failed   string               `json:"failed"`
	Error    string               `json:"error"`
	Form     map[string]string    `json:"form"`
}

var mailFields = []settings.Field{
	{Key: mail.KeyUncertainDest, Label: "Uncertain destination"},
	{Key: mail.KeyLoopsRemindHour, Label: "Loop reminder hour", Number: true, Whole: true},
}

type handlers struct{ d *web.Deps }

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(r chi.Router, d *web.Deps) {
	h := handlers{d}
	d.Authenticated(http.MethodGet, "/settings/mail", h.index)
	d.SettingsAt("/settings/mail", func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame)) {
		h.renderWith(w, r, status, page{}, mutate)
	})
	d.Authenticated(http.MethodPost, "/settings/mail/accounts", h.add)
	d.Authenticated(http.MethodPost, "/settings/mail/accounts/{id}", h.update)
	d.Authenticated(http.MethodPost, "/settings/mail/accounts/{id}/test", h.test)
	d.Authenticated(http.MethodPost, "/settings/mail/accounts/{id}/remove", h.remove)
	d.Authenticated(http.MethodPost, "/settings/mail/settings", h.saveSettings)
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func (h handlers) store(ctx context.Context) *connections.Store {
	if store := connections.DefaultFor(h.d.DB.Read); store != nil {
		return store
	}
	store, err := connections.New(h.d.DB, config.From(ctx).Host(), connections.DefaultRegistry())
	if err != nil {
		return nil
	}
	connections.SetDefault(h.d.DB.Read, store)
	return store
}

func (h handlers) accounts(ctx context.Context) ([]account, error) {
	store := h.store(ctx)
	var out []account
	for _, a := range config.From(ctx).Accounts() {
		password, _ := config.From(ctx).Password(a.Name)
		v := account{ID: a.Name, Label: a.Label, Preset: a.Preset, Login: a.Login, Enabled: a.Enabled, Pinned: a.Pinned,
			PasswordSet: password != "", AuthMethod: string(a.AuthMethod), State: string(connections.Untested)}
		if store != nil {
			view, err := store.Get(ctx, mailconn.ID(a.Name))
			switch {
			case errors.As(err, new(connections.UnknownID)):
			case err != nil:
				return nil, err
			default:
				v.State, v.Reason = string(view.State), view.Reason
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data page) {
	h.renderWith(w, r, status, data, func(f *tpl.Frame) { f.Toast = toast })
}

func (h handlers) renderWith(w http.ResponseWriter, r *http.Request, status int, data page, mutate func(*tpl.Frame)) {
	list, err := h.accounts(r.Context())
	if err != nil {
		panic(err)
	}
	data.Accounts, data.Presets = list, []string{config.ICloud, config.Gmail}
	if data.Indexes, err = mail.EmbedIndexes(r.Context(), h.d.DB); err != nil {
		panic(err)
	}
	if data.Fields, err = settings.LoadFields(r.Context(), h.d.DB.Read, mailFields); err != nil {
		panic(err)
	}
	if data.Failed == "mail" {
		settings.EchoFields(r.Context(), data.Fields, data.Form)
	}
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	mutate(f)
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
	var pinned *settings.PinnedError
	var encryption settings.EncryptionRequired
	var required *connections.EncryptedRequired
	var failedTest *connections.TestFailed
	if !errors.As(err, &why) && !errors.As(err, &invalid) && !errors.As(err, &pinned) && !errors.As(err, &encryption) &&
		!errors.As(err, &required) && !errors.As(err, &failedTest) {
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

func (h handlers) saveSettings(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	values := make(map[string]string, len(mailFields))
	for _, f := range mailFields {
		if value, ok := web.Value(form, f.Key); ok {
			values[f.Key] = value
		}
	}
	err := settings.SaveFields(h.ctx(r), h.d.DB, mailFields, values)
	if err != nil && h.refuse(w, r, "mail", values, err) {
		return
	}
	if err != nil {
		panic(err)
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Saved"}, page{})
}

func (h handlers) stored(ctx context.Context, id string) (config.AccountSpec, error) {
	a, ok := config.From(ctx).Account(id)
	switch {
	case !ok:
		return a, errNotFound
	case a.Pinned:
		return a, refusal(id + " is set by deployment")
	}
	return a, nil
}

var errNotFound = errors.New("not found")

func candidate(a config.AccountSpec, password string) connections.Candidate {
	cfg, _ := json.Marshal(map[string]string{
		"preset":      a.Preset,
		"login":       a.Login,
		"auth_method": string(a.AuthMethod),
		"enabled":     strconv.FormatBool(a.Enabled),
	})
	c := connections.Candidate{
		Label:  a.Label,
		Config: cfg,
		Capabilities: []connections.Capability{
			{Name: mailconn.CapabilityIMAP, Enabled: true},
			{Name: mailconn.CapabilitySMTP, Enabled: true},
		},
	}
	if password != "" {
		c.Credentials = []connections.CredentialInput{{Slot: mailconn.PasswordSlot(a.Name), Value: password}}
	}
	return c
}

func (h handlers) ensure(ctx context.Context, a config.AccountSpec) (created bool, err error) {
	store := h.store(ctx)
	if store == nil {
		return false, errors.New("connections are not available")
	}
	_, err = store.Get(ctx, mailconn.ID(a.Name))
	if err == nil {
		return false, nil
	}
	if !errors.As(err, new(connections.UnknownID)) {
		return false, err
	}
	return true, store.Ensure(ctx, mailconn.ID(a.Name), mailconn.Kind, a.Label, candidate(a, "").Capabilities)
}

func (h handlers) drop(ctx context.Context, a config.AccountSpec) {
	if store := h.store(ctx); store != nil {
		_ = store.Remove(ctx, mailconn.ID(a.Name))
	}
}

func (h handlers) add(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	field := func(name string) string { return strings.TrimSpace(web.FormString(form, name, "")) }
	kept := map[string]string{"id": field("id"), "label": field("label"), "preset": field("preset"), "login": field("login"), "auth_method": field("auth_method")}
	password := config.NormalizePassword(web.FormString(form, "password", ""))
	ctx := h.ctx(r)
	a, err := config.NewAccount(kept["id"], kept["label"], kept["preset"], kept["login"], true)
	if err == nil {
		a.AuthMethod, err = config.ParseAuthMethod(kept["auth_method"])
	}
	switch {
	case err != nil:
		err = refusal(err.Error())
	case !config.AuthAllowed(a.Preset, a.AuthMethod):
		err = refusal("XOAUTH2 is Gmail-only")
	case a.Login == "":
		err = refusal("login is required")
	case a.AuthMethod == config.AuthPassword && password == "":
		err = refusal("login and password are required")
	case !h.d.DB.Encrypted():
		err = settings.EncryptionRequired(mailconn.PasswordSlot(a.Name))
	default:
		if _, exists := config.From(ctx).Account(a.Name); exists {
			err = refusal(a.Name + " is already an account id")
		}
	}
	attempted := false
	if err == nil {
		attempted = true
		_, err = h.ensure(ctx, a)
	}
	if err == nil {
		_, err = h.store(ctx).TestAndSave(ctx, mailconn.ID(a.Name), candidate(a, password))
	}
	if err == nil {
		err = h.save(ctx, a)
	}
	if err != nil {
		if attempted {
			_ = config.DeleteStoredAccount(ctx, h.d.DB.Write, a.Name)
			h.drop(ctx, a)
			_ = config.From(ctx).LoadStoredAccounts(ctx, h.d.DB.Read)
		}
		h.fail(w, r, "new", kept, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account added", Description: "Restart the atrium units to sync it."}, page{})
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := h.stored(h.ctx(r), id)
	if errors.Is(err, errNotFound) {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	ctx := h.ctx(r)
	var a config.AccountSpec
	if err == nil {
		password := config.NormalizePassword(web.FormString(form, "password", ""))
		login := strings.TrimSpace(web.FormString(form, "login", current.Login))
		a, err = config.NewAccount(id, strings.TrimSpace(web.FormString(form, "label", current.Label)), current.Preset, login, web.FormString(form, "enabled", "") != "")
		if err == nil {
			a.AuthMethod, err = config.ParseAuthMethod(web.FormString(form, "auth_method", string(current.AuthMethod)))
		}
		if err == nil && !config.AuthAllowed(a.Preset, a.AuthMethod) {
			err = refusal("XOAUTH2 is Gmail-only")
		}
		if err == nil && a.Login == "" {
			err = refusal("login is required")
		}
		if err == nil && a.AuthMethod == config.AuthPassword && password == "" {
			_, known := config.From(ctx).Password(id)
			if !known {
				err = refusal("login and password are required")
			}
		}
		tested := a.Login != current.Login || password != "" || a.AuthMethod != current.AuthMethod
		if err == nil {
			_, err = h.ensure(ctx, a)
		}
		if err == nil && tested {
			_, err = h.store(ctx).TestAndSave(ctx, mailconn.ID(id), candidate(a, password))
		} else if err == nil {
			_, err = h.store(ctx).Save(ctx, mailconn.ID(id), candidate(a, password))
		}
		if err == nil {
			err = h.save(ctx, a)
		}
	}
	if err != nil {
		h.fail(w, r, id, nil, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account saved", Description: "Restart the atrium units to apply it."}, page{})
}

func (h handlers) save(ctx context.Context, a config.AccountSpec) error {
	if err := h.d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		return config.SaveStoredAccount(ctx, tx, a, stamp())
	}); err != nil {
		return err
	}
	return config.From(ctx).LoadStoredAccounts(ctx, h.d.DB.Read)
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := config.From(h.ctx(r)).Account(id); !ok {
		http.NotFound(w, r)
		return
	}
	ctx := h.ctx(r)
	store := h.store(ctx)
	if store == nil {
		panic(errors.New("connections are not available"))
	}
	view, err := store.Test(ctx, mailconn.ID(id))
	if err != nil {
		panic(err)
	}
	if view.State != connections.OK {
		reason := view.Reason
		if reason == "" {
			reason = "test failed"
		}
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Test failed", Description: reason}, page{Failed: id, Error: reason})
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connection works"}, page{})
}

func (h handlers) remove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, err := h.stored(h.ctx(r), id)
	if errors.Is(err, errNotFound) {
		http.NotFound(w, r)
		return
	}
	ctx := h.ctx(r)
	if err == nil {
		err = h.d.DB.WithTx(ctx, func(tx *sql.Tx) error {
			return config.DeleteStoredAccount(ctx, tx, id)
		})
	}
	if err == nil {
		if store := h.store(ctx); store != nil {
			err = store.Remove(ctx, mailconn.ID(id))
		}
	}
	if err == nil {
		err = config.From(ctx).LoadStoredAccounts(ctx, h.d.DB.Read)
	}
	if err != nil {
		h.fail(w, r, id, nil, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account removed", Description: "Its cached mail stays."}, page{})
}
