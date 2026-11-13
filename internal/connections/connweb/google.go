package connweb

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type googlePage struct {
	ClientID        string                       `json:"client_id"`
	ClientSecretSet bool                         `json:"client_secret_set"`
	Redirect        string                       `json:"redirect"`
	Capabilities    []connectionsapp.GoogleGrant `json:"capabilities"`
	Selected        string                       `json:"selected"`
	Health          string                       `json:"health"`
	Reason          string                       `json:"reason"`
	Error           string                       `json:"error"`
	Paste           string                       `json:"paste"`
}

func googleRedirect(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && strings.HasPrefix(r.Host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + googlePath + "/callback"
}

func (h handlers) googlePage(r *http.Request) googlePage {
	read, err := h.svc().OAuthSetup(r.Context(), connectionsapp.OAuthSetupInput{})
	if err != nil {
		panic(err)
	}
	data := read.Data
	return googlePage{ClientID: data.ClientID, ClientSecretSet: data.ClientSecretSet, Redirect: googleRedirect(r), Capabilities: data.Capabilities, Selected: "calendar", Health: data.Health, Reason: data.Reason}
}

func (h handlers) googleRender(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data googlePage) {
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "google.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) google(w http.ResponseWriter, r *http.Request) {
	h.googleRender(w, r, http.StatusOK, nil, h.googlePage(r))
}

func (h handlers) googleFailure(w http.ResponseWriter, r *http.Request, err error, title string) {
	failure := application.Classify(err)
	web.Fail(w, r, failure, func(status int) {
		if failure.Code == application.Refused {
			status = http.StatusUnprocessableEntity
		}
		page := h.googlePage(r)
		page.Error = web.Message(failure)
		h.googleRender(w, r, status, &tpl.Toast{Category: "error", Title: title, Description: page.Error}, page)
	})
}

func (h handlers) googleSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	if _, err := h.svc().OAuthConfigure(context.WithoutCancel(r.Context()), connectionsapp.OAuthConfigureInput{ClientID: web.FormString(form, "client_id", ""), ClientSecret: web.FormString(form, "client_secret", "")}); err != nil {
		h.googleFailure(w, r, err, "Not saved")
		return
	}
	http.Redirect(w, r, googlePath+"?saved=1", http.StatusSeeOther)
}

func (h handlers) googleStart(w http.ResponseWriter, r *http.Request) {
	in := connectionsapp.OAuthStartInput{Capability: web.FormString(web.FormValues(r), "capability", "calendar"), Redirect: googleRedirect(r)}
	change, err := h.svc().OAuthStart(context.WithoutCancel(r.Context()), in)
	if err != nil {
		h.googleFailure(w, r, err, "Not started")
		return
	}
	http.Redirect(w, r, change.Data.URL, http.StatusSeeOther)
}

func (h handlers) googleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	h.googleComplete(w, r, query.Get("state"), query.Get("code"), query.Get("error"))
}

func (h handlers) googlePaste(w http.ResponseWriter, r *http.Request) {
	pasted := web.FormString(web.FormValues(r), "paste", "")
	parsed, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil || parsed.Query().Get("code") == "" {
		page := h.googlePage(r)
		page.Error = "Paste the full URL you were redirected to, including the code."
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not connected", Description: page.Error}, page)
		return
	}
	h.googleComplete(w, r, parsed.Query().Get("state"), parsed.Query().Get("code"), parsed.Query().Get("error"))
}

func (h handlers) googleComplete(w http.ResponseWriter, r *http.Request, state, code, authErr string) {
	change, err := h.svc().OAuthComplete(context.WithoutCancel(r.Context()), connectionsapp.OAuthCompleteInput{State: state, Code: code, AuthError: authErr})
	if err != nil {
		h.googleFailure(w, r, err, "Not connected")
		return
	}
	http.Redirect(w, r, googlePath+"?connected="+change.Data.Name, http.StatusSeeOther)
}

func (h handlers) googleTest(w http.ResponseWriter, r *http.Request) {
	change, err := h.svc().Test(context.WithoutCancel(r.Context()), connectionsapp.TestInput{ID: "google/default"})
	if err != nil {
		h.googleFailure(w, r, err, "Not tested")
		return
	}
	view := change.Data[0]
	if view.State != "ok" && view.State != "expiring" {
		page := h.googlePage(r)
		page.Error = view.Reason
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not verified", Description: view.Reason}, page)
		return
	}
	h.googleRender(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connected", Description: "The refresh token works."}, h.googlePage(r))
}
