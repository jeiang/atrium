package integrations

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
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
	path         = "/settings/integrations"
	checkEvery   = 24 * time.Hour
	recheckAfter = 5 * time.Minute
)

var log = logx.Logger("atrium.integrations")

type page struct {
	Records          []Record          `json:"records"`
	Sections         []SectionView     `json:"sections"`
	Failed           string            `json:"failed"`
	Error            string            `json:"error"`
	HoldFor          string            `json:"hold_for"`
	Hold             module.Hold       `json:"hold"`
	SubmittedSection string            `json:"-"`
	SubmittedRecord  string            `json:"-"`
	Submitted        map[string]string `json:"-"`
	FieldError       string            `json:"field_error"`
	Saved            string            `json:"saved"`
}

const savedPrefix = "connection."

type handlers struct{ d *web.Deps }

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(r chi.Router, d *web.Deps) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		watch(d)
	}()
	d.OnClose(func() { <-done })
	h := handlers{d}
	d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
		if !f.Auth.Admin {
			return nil
		}
		rows, err := d.DB.Read.QueryContext(ctx, "SELECT name || ' ' || replace(state, '_', ' ') FROM integration_health WHERE state <> 'ok' ORDER BY name")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			f.IntegrationAlerts = append(f.IntegrationAlerts, name)
		}
		return rows.Err()
	})
	d.OnFrame(h.connections)
	d.Admin(http.MethodGet, path, h.index)
	d.Admin(http.MethodPost, path+"/{name}/test", h.test)
	d.Admin(http.MethodPost, path+"/{name}/settings", h.saveSettings)
	d.Admin(http.MethodPost, path+"/sections/{name}", h.saveSection)
	d.Admin(http.MethodPost, path+"/{name}/disconnect", h.disconnect)
	d.Admin(http.MethodGet, googlePath, h.google)
	d.Admin(http.MethodPost, googlePath, h.googleSave)
	d.Admin(http.MethodPost, googlePath+"/start", h.googleStart)
	d.Admin(http.MethodGet, googleCallbackPath, h.googleCallback)
	d.Admin(http.MethodPost, googlePastePath, h.googlePaste)
	d.Admin(http.MethodPost, googleTestPath, h.googleTest)
}

func watch(d *web.Deps) {
	ctx, cancel := context.WithCancel(d.Ctx)
	defer cancel()
	go func() {
		<-d.Stopping
		cancel()
	}()
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	checked := map[string]time.Time{}
	for ctx.Err() == nil {
		if err := CheckAll(ctx, d.DB, d.Env, stamp()); err != nil && ctx.Err() == nil {
			log.Warn("integration health check failed", logx.Err, err.Error())
		}
		recheck(ctx, d, t.C, checked)
	}
}

func recheck(ctx context.Context, d *web.Deps, daily <-chan time.Time, checked map[string]time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-daily:
			return
		case name := <-transport.Failed():
			if _, known := Get(name); !known || time.Since(checked[name]) < recheckAfter || !working(ctx, d, name) {
				continue
			}
			state, err := Check(ctx, d.DB, d.Env, name, stamp())
			if err != nil && ctx.Err() == nil {
				log.Warn("integration health check failed", logx.Integration, name, logx.Err, err.Error())
			}
			if state == OK {
				checked[name] = time.Now()
			}
		}
	}
}

func working(ctx context.Context, d *web.Deps, name string) bool {
	var state string
	err := d.DB.Read.QueryRowContext(ctx, "SELECT state FROM integration_health WHERE name = ?", name).Scan(&state)
	return err != nil || state != Failing && state != NeedsReauth
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data page) {
	records, err := Records(r.Context(), h.d.DB.Read)
	if err != nil {
		panic(err)
	}
	data.Records = records
	if data.Sections, err = LoadSections(r.Context(), h.d.DB.Read); err != nil {
		panic(err)
	}
	h.echo(r.Context(), &data)
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "integrations.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) owned(ctx context.Context, owner string) (page, error) {
	var view page
	all, err := Records(ctx, h.d.DB.Read)
	if err != nil {
		return view, err
	}
	for _, record := range all {
		if record.Module == owner {
			view.Records = append(view.Records, record)
		}
	}
	sections, err := LoadSections(ctx, h.d.DB.Read)
	if err != nil {
		return view, err
	}
	for _, section := range sections {
		inCard := slices.ContainsFunc(view.Records, func(r Record) bool { return r.Name == section.Integration })
		if inCard || section.Integration == "" && section.Module == owner {
			view.Sections = append(view.Sections, section)
		}
	}
	return view, nil
}

func (h handlers) connections(ctx context.Context, f *tpl.Frame) error {
	owner, ok := strings.CutPrefix(f.Path, "/settings/")
	if f.Page != "settings" || !ok || f.Path == path {
		return nil
	}
	view, err := h.owned(ctx, owner)
	if err != nil || len(view.Records)+len(view.Sections) == 0 {
		return err
	}
	if name, ok := strings.CutPrefix(f.Saved, savedPrefix); ok {
		view.Saved = name
		if i, known := Get(name); known {
			view.HoldFor, view.Hold = name, h.held(ctx, i.gate())
		}
	}
	f.Connections = view
	return nil
}

func owner(name string) string {
	if i, ok := Get(name); ok {
		return i.Module
	}
	s, _ := SectionNamed(name)
	return s.Module
}

func (h handlers) back(w http.ResponseWriter, r *http.Request, name string) bool {
	back := web.FormString(web.FormValues(r), "back", "")
	if back != "/settings/"+owner(name) {
		return false
	}
	w.Header().Set("Location", back+"?saved="+savedPrefix+name)
	w.WriteHeader(http.StatusSeeOther)
	return true
}

func (h handlers) echo(ctx context.Context, data *page) {
	for i := range data.Sections {
		if data.Sections[i].Name == data.SubmittedSection {
			settings.EchoFields(ctx, data.Sections[i].Fields, data.Submitted)
		}
	}
	for i := range data.Records {
		if data.Records[i].Name == data.SubmittedRecord {
			settings.EchoFields(ctx, data.Records[i].Fields, data.Submitted)
		}
	}
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, nil, page{})
}

func (h handlers) refuse(w http.ResponseWriter, r *http.Request, name string, err error, submitted ...page) bool {
	var failed *TestFailed
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var scope *settings.NotEditable
	var host settings.HostOwned
	var encryption settings.EncryptionRequired
	var empty settings.EmptyValue
	var unknown Unknown
	var field settings.FieldInvalid
	switch {
	case errors.As(err, &unknown):
		http.NotFound(w, r)
	case errors.As(err, &failed), errors.As(err, &invalid), errors.As(err, &pinned), errors.As(err, &scope), errors.As(err, &host), errors.As(err, &encryption), errors.As(err, &empty):
		data := page{Failed: name, Error: err.Error()}
		if len(submitted) > 0 {
			data.SubmittedSection = submitted[0].SubmittedSection
			data.SubmittedRecord = submitted[0].SubmittedRecord
			data.Submitted = submitted[0].Submitted
		}
		if errors.As(err, &field) {
			data.FieldError = field.Key
		}
		toast := &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}
		back := web.FormString(web.FormValues(r), "back", "")
		if back == "/settings/"+owner(name) && h.d.SettingsPage(w, r, http.StatusUnprocessableEntity, back, func(f *tpl.Frame) {
			view, loadErr := h.owned(r.Context(), owner(name))
			if loadErr != nil {
				panic(loadErr)
			}
			view.Failed, view.Error, view.FieldError = data.Failed, data.Error, data.FieldError
			view.SubmittedSection, view.SubmittedRecord, view.Submitted = data.SubmittedSection, data.SubmittedRecord, data.Submitted
			h.echo(r.Context(), &view)
			f.Connections, f.Toast = view, toast
		}) {
			return true
		}
		h.render(w, r, http.StatusUnprocessableEntity, toast, data)
	default:
		return false
	}
	return true
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func (h handlers) enable(ctx context.Context, name string) module.Hold {
	set := module.SetFrom(h.d.Ctx)
	if set == nil || name == "" {
		return module.Hold{}
	}
	hold, err := set.Enable(ctx, h.d.DB, name)
	if err != nil {
		panic(err)
	}
	return hold
}

func (h handlers) held(ctx context.Context, name string) module.Hold {
	set := module.SetFrom(h.d.Ctx)
	if set == nil {
		return module.Hold{}
	}
	hold, err := set.Held(ctx, h.d.DB.Read, name)
	if err != nil {
		panic(err)
	}
	return hold
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	form := web.FormValues(r)
	secret := web.FormString(form, "secret", "")
	saved, err := TestAndSave(h.ctx(r), h.d.DB, h.d.Env, name, Candidate{URL: web.FormString(form, "url", ""), Secret: secret, HasSecret: secret != ""}, "ui", stamp())
	if err != nil && h.refuse(w, r, name, err) {
		return
	}
	if err != nil {
		panic(err)
	}
	data := page{}
	if i, _ := Get(name); saved {
		data.HoldFor, data.Hold = name, h.enable(h.ctx(r), i.gate())
	}
	if h.back(w, r, name) {
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connection works"}, data)
}

func fieldValues(fields []Field, form url.Values) map[string]string {
	values := make(map[string]string, len(fields))
	for _, f := range fields {
		if !f.Multi {
			if value, ok := web.Value(form, f.Key); ok {
				values[f.Key] = value
			}
			continue
		}
		picked := make([]string, 0, len(f.Choices))
		for _, value := range form[f.Key] {
			if value != "" && value != "none" {
				picked = append(picked, value)
			}
		}
		if len(picked) == 0 {
			picked = append(picked, "none")
		}
		values[f.Key] = strings.Join(picked, ",")
	}
	return values
}

func (h handlers) saveSection(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	s, ok := SectionNamed(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	values := fieldValues(s.Fields, form)
	err := SaveSection(h.ctx(r), h.d.DB, name, values)
	if err != nil && h.refuse(w, r, name, err, page{SubmittedSection: name, Submitted: fieldValues(s.Fields, form)}) {
		return
	}
	if err != nil {
		panic(err)
	}
	if h.back(w, r, name) {
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Saved"}, page{})
}

func (h handlers) saveSettings(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	i, ok := Get(name)
	if !ok || len(i.Fields) == 0 {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	values := fieldValues(i.Fields, form)
	err := SaveFields(h.ctx(r), h.d.DB, name, values)
	if err != nil && h.refuse(w, r, name, err, page{SubmittedRecord: name, Submitted: fieldValues(i.Fields, form)}) {
		return
	}
	if err != nil {
		panic(err)
	}
	if h.back(w, r, name) {
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Saved"}, page{})
}

func (h handlers) disconnect(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	err := Disconnect(h.ctx(r), h.d.DB, name)
	if err != nil && h.refuse(w, r, name, err) {
		return
	}
	if err != nil {
		panic(err)
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Disconnected", Description: "Restart the atrium units to stop the module."}, page{})
}
