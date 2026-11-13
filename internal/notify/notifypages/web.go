package notifypages

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	moduleowner "github.com/jeiang/atrium/internal/module"
	notifyapp "github.com/jeiang/atrium/internal/notify/application"
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

type inboxView struct {
	Notifications []notifyapp.Notification `json:"notifications"`
}

const (
	page      = "/settings/notifications"
	inboxPage = "/inbox"
)

type handlers struct {
	d       *web.Deps
	service *notifyapp.Service
}

func Module(factory func(*web.Deps) *notifyapp.Service) web.Module {
	return func(_ chi.Router, d *web.Deps) { serve(d, factory(d)) }
}
func serve(d *web.Deps, service *notifyapp.Service) {
	h := handlers{d: d, service: service}
	d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
		labels, err := service.Failing(ctx)
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
}

func (h handlers) ctx(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
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
	found, err := h.service.Configuration(r.Context(), notifyapp.Empty{})
	if err != nil {
		panic(err)
	}
	data := found.Data
	data.Error = problem
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
	found, err := h.service.Inbox(r.Context(), notifyapp.InboxInput{Limit: 100})
	if err != nil {
		panic(err)
	}
	f, err := h.d.Render.Frame(r, "inbox", inboxView{Notifications: found.Data.Items})
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
	var e *application.Error
	var disabled *moduleowner.OwnerDisabled
	return errors.As(err, &disabled) || (errors.As(err, &e) && (e.Code == application.Refused || e.Code == application.Invalid))
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
	_, err := h.service.ChannelContent(h.ctx(r), notifyapp.ChannelContentInput{Name: chi.URLParam(r, "name"), Content: web.FormString(form, "content", "")})
	h.done(w, r, err, "Saved")
}

func (h handlers) resend(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, err = h.service.Resend(h.ctx(r), notifyapp.ResendInput{ID: id})
	var applicationError *application.Error
	if errors.As(err, &applicationError) && applicationError.Code == application.NotFound {
		http.NotFound(w, r)
		return
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
	_, err := h.service.Subscribe(h.ctx(r), notifyapp.SubscribeInput{Endpoint: web.FormString(form, "endpoint", ""), P256DH: web.FormString(form, "p256dh", ""), Auth: web.FormString(form, "auth", ""), Label: notifyapp.PushLabel(r.UserAgent())})
	h.done(w, r, err, "Push enabled on this device")
}

func (h handlers) unsubscribe(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	_, err := h.service.Unsubscribe(h.ctx(r), notifyapp.UnsubscribeInput{Endpoint: web.FormString(form, "endpoint", "")})
	h.done(w, r, err, "Push disabled on this device")
}

func (h handlers) removeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, err = h.service.Unsubscribe(h.ctx(r), notifyapp.UnsubscribeInput{DeviceID: &id})
	h.done(w, r, err, "Device removed")
}

func (h handlers) pushTest(w http.ResponseWriter, r *http.Request) {
	_, err := h.service.Test(h.ctx(r), notifyapp.Empty{})
	h.done(w, r, err, "Test sent")
}
