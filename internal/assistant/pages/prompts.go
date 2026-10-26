package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type PromptView struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Text   string `json:"text"`
	Custom bool   `json:"custom"`
	Pinned bool   `json:"pinned"`
	Error  string `json:"error"`
}

type PromptsData struct {
	Preamble string       `json:"preamble"`
	Prompts  []PromptView `json:"prompts"`
	Strip    bool         `json:"strip"`
}

func editable(key string) (prompts.Editable, bool) {
	for _, e := range prompts.Editables {
		if e.Key == key {
			return e, true
		}
	}
	return prompts.Editable{}, false
}

func (p *pages) promptsPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, draft *PromptView) {
	views := make([]PromptView, len(prompts.Editables))
	for i, e := range prompts.Editables {
		found, err := settings.Default.Resolve(r.Context(), p.deps.DB.Read, e.Key, 0)
		check(err)
		views[i] = PromptView{Key: e.Key, Label: e.Label, Text: e.Default, Custom: found.Value != "", Pinned: found.Pinned()}
		if found.Value != "" {
			views[i].Text = found.Value
		}
		if draft != nil && draft.Key == e.Key {
			views[i].Text, views[i].Error = draft.Text, draft.Error
		}
	}
	p.settings(w, r, status, "prompts.gohtml", PromptsData{Preamble: prompts.Preamble, Prompts: views, Strip: toast != nil && status == http.StatusOK}, toast)
}

func (p *pages) promptsIndex(w http.ResponseWriter, r *http.Request) {
	var toast *tpl.Toast
	if e, ok := editable(web.QueryValue(r.URL.Query(), "saved")); ok {
		toast = notice("success", "Prompt saved", e.Label)
	} else if e, ok := editable(web.QueryValue(r.URL.Query(), "reset")); ok {
		toast = notice("success", "Prompt reset", e.Label)
	}
	p.promptsPage(w, r, http.StatusOK, toast, nil)
}

func (p *pages) promptChange(w http.ResponseWriter, r *http.Request, e prompts.Editable, text string, reset bool) {
	ctx := audit.With(detached(r), "operator", audit.UI)
	var err error
	if reset || text == e.Default {
		err = settings.Default.Unset(ctx, p.deps.DB.Write, e.Key, 0)
	} else {
		err = settings.Default.Set(ctx, p.deps.DB.Write, e.Key, text, 0)
	}
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	if errors.As(err, &invalid) || errors.As(err, &pinned) {
		p.promptsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), &PromptView{Key: e.Key, Text: text, Error: err.Error()})
		return
	}
	check(err)
	param := "saved"
	if reset {
		param = "reset"
	}
	settingsRedirect(w, "/settings/prompts?"+param+"="+url.QueryEscape(e.Key))
}

func (p *pages) promptSave(w http.ResponseWriter, r *http.Request) {
	e, ok := editable(chi.URLParam(r, "key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	p.promptChange(w, r, e, strings.ReplaceAll(web.FormString(form, "text", ""), "\r\n", "\n"), false)
}

func (p *pages) promptReset(w http.ResponseWriter, r *http.Request) {
	e, ok := editable(chi.URLParam(r, "key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	p.promptChange(w, r, e, e.Default, true)
}
