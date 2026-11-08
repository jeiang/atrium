package pages

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

func contextLimitsOf(m pricing.Model) []int64 {
	out := make([]int64, 0, len(assistant.ContextLimits))
	for _, n := range assistant.ContextLimits {
		if n <= m.ContextWindow {
			out = append(out, n)
		}
	}
	return out
}

type effortOption struct {
	Value    string
	Disabled bool
}

func composerData(ctx context.Context, modelID, effort, thinking string, limit int64, webSearch bool, costCap *float64, live int64) map[string]any {
	models := pricing.Models()
	efforts, limits, known := []effortOption{}, assistant.ContextLimits, false
	for _, e := range pricing.Efforts {
		efforts = append(efforts, effortOption{Value: e})
	}
	thinkingOptions := []pricing.Thinking{}
	cliff := false
	if m, err := pricing.Lookup(modelID); err == nil {
		known, limits = true, contextLimitsOf(m)
		effort = threads.SnapEffort(m, effort)
		if !m.ValidThinking(thinking, effort) {
			thinking = m.DefaultThinking()
		}
		thinkingOptions = m.Thinking
		efforts = efforts[:0]
		for _, e := range m.Efforts {
			efforts = append(efforts, effortOption{Value: e, Disabled: !m.ValidThinking(thinking, e)})
		}
		if limit == 0 {
			limit = assistant.DefaultContextLimit
		}
		limit = threads.SnapContextLimit(m, limit)
		cliff = m.ID == pricing.HaikuID && limit > pricing.TierThreshold
	}
	return map[string]any{
		"models": models, "model": modelID, "known": known,
		"efforts": efforts, "effort": effort,
		"context_limits": limits, "context": limit,
		"thinking": thinking, "thinking_options": thinkingOptions, "effort_fixed": threads.EffortFixed(thinking),
		"web_search": webSearch, "cost_cap": costCap,
		"cliff": cliff, "coding": module.IsEnabled(ctx, "sandbox"),
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
	return composerData(r.Context(), defaults.Model, effort, defaults.Thinking, limit, defaults.WebSearch, defaults.CostCapUSD, 0)
}

func (p *pages) composerOptions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	model, err := threads.CleanModel(web.QueryValue(query, "model"))
	if err != nil {
		p.failure(w, r, "Unknown model", err.Error())
		return
	}
	effort := web.QueryValue(query, "effort")
	thinking := web.QueryValue(query, "thinking")
	limit, _ := threads.CleanContextLimit(web.QueryValue(query, "context_limit"))
	if web.QueryValue(query, "reset") == "1" {
		effort, thinking, limit = pricing.Medium, "", assistant.DefaultContextLimit
	}
	webSearch := web.QueryValue(query, "web_search") != "0"
	var costCap *float64
	if raw := strings.TrimSpace(web.QueryValue(query, "cost_cap")); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			costCap = &value
		}
	}
	p.fragment(w, r, http.StatusOK, "_composer_options.gohtml", map[string]any{
		"composer": composerData(r.Context(), model.ID, effort, thinking, limit, webSearch, costCap, 0),
	})
}
