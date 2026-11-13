package pages

import (
	"net/http"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
)

func (p *pages) composerOptions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	read, err := p.assistant().Configuration(r.Context(), asstapp.ConfigurationInput{Composer: &asstapp.ComposerInput{
		Model:        web.QueryValue(query, "model"),
		Effort:       web.QueryValue(query, "effort"),
		Thinking:     web.QueryValue(query, "thinking"),
		ContextLimit: web.QueryValue(query, "context_limit"),
		WebSearch:    web.QueryValue(query, "web_search") != "0",
		CostCap:      web.QueryValue(query, "cost_cap"),
		Reset:        web.QueryValue(query, "reset") == "1",
	}})
	if err != nil {
		web.Fail(w, r, err, func(status int) {
			p.failureStatus(w, r, status, "Unknown model", web.Message(application.Classify(err)))
		})
		return
	}
	if web.FragmentNotModified(w, r, read.Revision) {
		return
	}
	p.fragment(w, r, http.StatusOK, "_composer_options.gohtml", struct {
		Composer asstapp.ComposerView `json:"composer"`
	}{Composer: read.Data.Composer})
}
