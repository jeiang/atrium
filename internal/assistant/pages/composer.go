package pages

import (
	"net/http"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/web"
)

func contextLimitsOf(m pricing.Model) []int64 {
	out := make([]int64, 0, len(threads.ContextLimits))
	for _, n := range threads.ContextLimits {
		if n <= m.ContextWindow {
			out = append(out, n)
		}
	}
	return out
}

func composerData(modelID, effort string, limit, live int64) map[string]any {
	models := pricing.Models()
	efforts, limits, known := threads.Efforts[:], threads.ContextLimits[:], false
	if m, err := pricing.Lookup(modelID); err == nil {
		known, efforts, limits = true, m.Efforts, contextLimitsOf(m)
		if !slices.Contains(efforts, effort) {
			effort = efforts[0]
			if slices.Contains(efforts, threads.Medium) {
				effort = threads.Medium
			}
		}
		if !slices.Contains(limits, limit) {
			limit = limits[len(limits)-1]
			if slices.Contains(limits, threads.DefaultContextLimit) {
				limit = threads.DefaultContextLimit
			}
		}
	}
	return map[string]any{
		"models": models, "model": modelID, "known": known,
		"efforts": efforts, "effort": effort,
		"context_limits": limits, "context": limit,
		"live": live,
	}
}

func (p *pages) newThreadComposer(r *http.Request, effort string, limit int64) map[string]any {
	defaults, err := threads.Defaults(r.Context(), p.deps.DB.Read)
	check(err)
	if effort == "" {
		effort = defaults.Effort
	}
	if limit == 0 {
		limit = defaults.ContextLimit
	}
	return composerData(defaults.Model, effort, limit, 0)
}

func (p *pages) composerOptions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	model, err := threads.CleanModel(web.QueryValue(query, "model"))
	if err != nil {
		p.failure(w, r, "Unknown model", err.Error())
		return
	}
	limit, _ := threads.CleanContextLimit(web.QueryValue(query, "context_limit"))
	p.fragment(w, r, http.StatusOK, "_composer_options.gohtml", map[string]any{
		"composer": composerData(model.ID, web.QueryValue(query, "effort"), limit, 0),
	})
}
