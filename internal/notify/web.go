package notify

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

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
	Notifications []Notification `json:"notifications"`
	Channels      []Channel      `json:"channels"`
	URL           string         `json:"url"`
	NtfySh        bool           `json:"ntfy_sh"`
	Encrypted     bool           `json:"encrypted"`
	Error         string         `json:"error"`
}

const page = "/settings/notifications"

type handlers struct{ d *web.Deps }

func Module(r chi.Router, d *web.Deps) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchStale(d.Ctx, d.DB, d.Env, d.Stopping)
	}()
	d.OnClose(func() { <-done })
	h := handlers{d}
	d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
		if !f.Auth.Admin {
			return nil
		}
		rows, err := d.DB.Read.QueryContext(ctx, "SELECT name FROM ntfy_channels WHERE failures >= 3 ORDER BY name")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			f.NotifyFailing = append(f.NotifyFailing, name)
		}
		return rows.Err()
	})
	d.Admin(http.MethodGet, page, h.index)
	d.Admin(http.MethodPost, page+"/account", h.connect)
	d.Admin(http.MethodPost, page+"/probe", h.probe)
	d.Admin(http.MethodPost, page+"/channels/{name}/test", h.test)
	d.Admin(http.MethodPost, page+"/channels/{name}/content", h.content)
	d.Admin(http.MethodPost, page+"/channels/{name}/reset", h.reset)
	d.Admin(http.MethodPost, page+"/{id}/resend", h.resend)
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, problem string) {
	ctx := r.Context()
	list, err := Recent(ctx, h.d.DB.Read, 100)
	if err != nil {
		panic(err)
	}
	channels, err := ListChannels(ctx, h.d.DB.Write)
	if err != nil {
		panic(err)
	}
	data := listing{Notifications: list, Encrypted: h.d.DB.Encrypted(), Error: problem}
	base, baseErr := baseURL(ctx, h.d.DB.Read)
	if baseErr == nil {
		data.URL, data.NtfySh = base.String(), IsNtfySh(base)
	}
	for _, c := range channels {
		c.ShowsText = c.FullText()
		if baseErr == nil {
			c.Subscribe = "ntfy://" + base.Host + "/" + url.PathEscape(c.Topic)
		}
		data.Channels = append(data.Channels, c)
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

func (h handlers) done(w http.ResponseWriter, r *http.Request, err error, title string) {
	var account AccountError
	var channel ChannelError
	var encryption settings.EncryptionRequired
	var pinned *settings.PinnedError
	if errors.As(err, &account) || errors.As(err, &channel) || errors.As(err, &encryption) || errors.As(err, &pinned) {
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, err.Error())
		return
	}
	if err != nil {
		panic(err)
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: title}, "")
}

func (h handlers) connect(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	err := Connect(h.ctx(r), h.d.DB, web.FormString(form, "username", ""), web.FormString(form, "password", ""), "ui")
	h.done(w, r, err, "Account connected")
}

func (h handlers) probe(w http.ResponseWriter, r *http.Request) {
	h.done(w, r, ProbeAll(h.ctx(r), h.d.DB), "Privacy checked")
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !known(name) {
		http.NotFound(w, r)
		return
	}
	sent, err := Send(h.ctx(r), h.d.DB.Write, h.d.Env, Message{Channel: name, Title: "atrium test", Body: "Test message for the " + name + " channel."})
	if err == nil && !sent.Delivered {
		err = ChannelError("Test not delivered: " + sent.Error)
	}
	h.done(w, r, err, "Test sent")
}

func (h handlers) content(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	h.done(w, r, SetContent(h.ctx(r), h.d.DB.Write, chi.URLParam(r, "name"), web.FormString(form, "content", "")), "Saved")
}

func (h handlers) reset(w http.ResponseWriter, r *http.Request) {
	h.done(w, r, ResetChannel(h.ctx(r), h.d.DB, chi.URLParam(r, "name")), "Channel reset")
}

func (h handlers) resend(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sent, err := Resend(h.ctx(r), h.d.DB.Write, h.d.Env, id)
	if err == nil && !sent.Delivered {
		err = ChannelError("Not delivered: " + sent.Error)
	}
	h.done(w, r, err, "Resent")
}
