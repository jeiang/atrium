package notify

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/db"
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

type listing struct {
	Channels []Channel   `json:"channels"`
	Error    string      `json:"error"`
	Push     pushListing `json:"push"`
	Phone    any         `json:"phone"`
}

type pushListing struct {
	Key     string               `json:"key"`
	Fields  []settings.FieldView `json:"fields"`
	Problem string               `json:"problem"`
	Devices []PushDevice         `json:"devices"`
}

var subjectFields = []settings.Field{{Key: KeyPushSubject, Label: "Push subject"}}

type inboxView struct {
	Notifications []Notification `json:"notifications"`
}

const (
	page      = "/settings/notifications"
	inboxPage = "/inbox"
)

type Section func(context.Context, db.Querier) (any, error)

type handlers struct {
	d     *web.Deps
	phone Section
}

func Module(phone Section, budget BudgetStatus) web.Module {
	return func(_ chi.Router, d *web.Deps) { serve(d, phone, budget) }
}

func serve(d *web.Deps, phone Section, budget BudgetStatus) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchStale(d.Ctx, d.DB, d.Env, d.Stopping, budget)
	}()
	d.OnClose(func() { <-done })
	h := handlers{d, phone}
	d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
		if !f.Auth.Admin {
			return nil
		}
		labels, err := failingDevices(ctx, d.DB.Read)
		f.NotifyFailing = labels
		return err
	})
	d.Admin(http.MethodGet, page, h.index)
	d.Admin(http.MethodPost, page+"/channels/{name}/content", h.content)
	d.Admin(http.MethodPost, page+"/push/subscriptions", h.subscribe)
	d.Admin(http.MethodPost, page+"/push/subject", h.pushSubject)
	d.Admin(http.MethodPost, page+"/push/unsubscribe", h.unsubscribe)
	d.Admin(http.MethodPost, page+"/push/test", h.pushTest)
	d.Admin(http.MethodPost, page+"/push/{id}/remove", h.removeDevice)
	d.Admin(http.MethodGet, inboxPage, h.inbox)
	d.Admin(http.MethodPost, inboxPage+"/{id}/resend", h.resend)
	d.APIAdmin(http.MethodGet, "/inbox", h.apiInbox)
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, problem string) {
	ctx := r.Context()
	data := listing{Error: problem}
	var err error
	if data.Channels, err = ListChannels(ctx, h.d.DB.Read); err != nil {
		panic(err)
	}
	if data.Push.Devices, err = Devices(ctx, h.d.DB.Read); err != nil {
		panic(err)
	}
	if data.Push.Fields, err = settings.LoadFields(ctx, h.d.DB.Read, subjectFields); err != nil {
		panic(err)
	}
	if status == http.StatusUnprocessableEntity && r.URL.Path == page+"/push/subject" {
		form := web.FormValues(r)
		settings.EchoFields(ctx, data.Push.Fields, map[string]string{KeyPushSubject: web.FormString(form, KeyPushSubject, "")})
	}
	if data.Push.Key, err = PushPublicKey(ctx, h.d.DB.Write); err != nil {
		data.Push.Problem = err.Error()
	}
	if data.Phone, err = h.phone(ctx, h.d.DB.Read); err != nil {
		panic(err)
	}
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "notifications.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, nil, "")
}

func (h handlers) renderInbox(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast) {
	list, err := Recent(r.Context(), h.d.DB.Read, 100)
	if err != nil {
		panic(err)
	}
	f, err := h.d.Render.Frame(r, "inbox", inboxView{Notifications: list})
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "inbox.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) inbox(w http.ResponseWriter, r *http.Request) {
	h.renderInbox(w, r, http.StatusOK, nil)
}

func refused(err error) bool {
	var channel ChannelError
	var push PushError
	var pinned *settings.PinnedError
	return errors.As(err, &channel) || errors.As(err, &push) || errors.As(err, &pinned)
}

func (h handlers) done(w http.ResponseWriter, r *http.Request, err error, title string) {
	if refused(err) {
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, err.Error())
		return
	}
	if err != nil {
		panic(err)
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: title}, "")
}

func (h handlers) content(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	h.done(w, r, SetContent(h.ctx(r), h.d.DB.Write, chi.URLParam(r, "name"), web.FormString(form, "content", "")), "Saved")
}

func (h handlers) pushSubject(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	err := settings.SaveFields(h.ctx(r), h.d.DB, subjectFields, map[string]string{KeyPushSubject: web.FormString(form, KeyPushSubject, "")})
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	if errors.As(err, &invalid) || errors.As(err, &pinned) {
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, err.Error())
		return
	}
	if err != nil {
		panic(err)
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Saved"}, "")
}

func (h handlers) resend(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sent, err := Resend(h.ctx(r), h.d.DB.Write, id)
	if err == nil && !sent.Delivered {
		err = ChannelError("Not delivered: " + sent.Error)
	}
	if refused(err) {
		h.renderInbox(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not resent", Description: err.Error()})
		return
	}
	if err != nil {
		panic(err)
	}
	h.renderInbox(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Resent"})
}

func (h handlers) subscribe(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	err := Subscribe(h.ctx(r), h.d.DB.Write, web.FormString(form, "endpoint", ""), web.FormString(form, "p256dh", ""), web.FormString(form, "auth", ""), PushLabel(r.UserAgent()))
	h.done(w, r, err, "Push enabled on this device")
}

func (h handlers) unsubscribe(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	h.done(w, r, Unsubscribe(h.ctx(r), h.d.DB.Write, web.FormString(form, "endpoint", "")), "Push disabled on this device")
}

func (h handlers) removeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.done(w, r, RemoveDevice(h.ctx(r), h.d.DB.Write, id), "Device removed")
}

func (h handlers) pushTest(w http.ResponseWriter, r *http.Request) {
	sent, failure, err := PushTest(h.ctx(r), h.d.DB.Write)
	if err == nil && sent == 0 {
		if failure == "" {
			failure = NoDevices
		}
		err = PushError("Test not delivered: " + failure)
	}
	h.done(w, r, err, "Test sent")
}
