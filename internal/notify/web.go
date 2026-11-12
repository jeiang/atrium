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
	moduleowner "github.com/jeiang/atrium/internal/module"
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
	Key     string       `json:"key"`
	Problem string       `json:"problem"`
	Devices []PushDevice `json:"devices"`
}

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

func Module(phone Section) web.Module {
	return func(_ chi.Router, d *web.Deps) { serve(d, phone) }
}

func serve(d *web.Deps, phone Section) {
	h := handlers{d, phone}
	d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
		labels, err := failingDevices(ctx, d.DB.Read)
		f.NotifyFailing = labels
		return err
	})
	d.Authenticated(http.MethodGet, page, h.index)
	d.Authenticated(http.MethodPost, page+"/channels/{name}/content", h.owned(h.content))
	d.Authenticated(http.MethodPost, page+"/push/subscriptions", h.owned(h.subscribe))
	d.Authenticated(http.MethodPost, page+"/push/unsubscribe", h.owned(h.unsubscribe))
	d.Authenticated(http.MethodPost, page+"/push/test", h.owned(h.pushTest))
	d.Authenticated(http.MethodPost, page+"/push/{id}/remove", h.owned(h.removeDevice))
	d.Authenticated(http.MethodGet, inboxPage, h.inbox)
	d.Authenticated(http.MethodPost, inboxPage+"/{id}/resend", h.owned(h.resend))
	d.API(http.MethodGet, "/inbox", h.apiInbox)
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func (h handlers) owned(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, err := moduleowner.BindOwner(r.Context(), moduleowner.Notifications)
		if err != nil {
			h.done(w, r, err, "")
			return
		}
		next(w, r.WithContext(ctx))
	}
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
	if data.Push.Key, err = PushPublicKey(ctx, h.d.DB); err != nil {
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
	var disabled *moduleowner.OwnerDisabled
	return errors.As(err, &channel) || errors.As(err, &push) || errors.As(err, &pinned) || errors.As(err, &disabled)
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

func (h handlers) resend(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sent, err := Resend(h.ctx(r), h.d.DB, id)
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
	sent, failure, err := PushTest(h.ctx(r), h.d.DB)
	if err == nil && sent == 0 {
		if failure == "" {
			failure = NoDevices
		}
		err = PushError("Test not delivered: " + failure)
	}
	h.done(w, r, err, "Test sent")
}
