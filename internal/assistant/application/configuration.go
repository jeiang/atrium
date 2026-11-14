package application

import (
	"context"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
)

const ConfigurationName application.Name = "assistant.configuration"

type EffortOption struct {
	Value    string
	Disabled bool
}

type ComposerView struct {
	Models          []pricing.Model    `json:"models"`
	Model           string             `json:"model"`
	Known           bool               `json:"known"`
	Efforts         []EffortOption     `json:"efforts"`
	Effort          string             `json:"effort"`
	ContextLimits   []int64            `json:"context_limits"`
	Context         int64              `json:"context"`
	Thinking        string             `json:"thinking"`
	ThinkingOptions []pricing.Thinking `json:"thinking_options"`
	EffortFixed     bool               `json:"effort_fixed"`
	WebSearch       bool               `json:"web_search"`
	CostCap         *float64           `json:"cost_cap"`
	Cliff           bool               `json:"cliff"`
	Coding          bool               `json:"coding"`
	Live            int64              `json:"live"`
}

type ComposerInput struct {
	Model        string
	Effort       string
	Thinking     string
	ContextLimit string
	WebSearch    bool
	CostCap      string
	Reset        bool
}

type ConfigurationInput struct {
	Composer *ComposerInput
}

type ModelConfiguration struct {
	Models        []pricing.Model `json:"models"`
	Defaults      threads.Options `json:"defaults"`
	Efforts       []string        `json:"efforts"`
	ContextLimits []int64         `json:"context_limits"`
	Composer      ComposerView    `json:"composer"`
}

type ComposerState struct {
	Model        string
	Effort       string
	Thinking     string
	ContextLimit int64
	WebSearch    bool
	CostCap      *float64
	Live         int64
}

func DescribeConfiguration() application.Definition {
	return application.Describe[ConfigurationInput, ModelConfiguration](ConfigurationName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func contextLimitsOf(m pricing.Model) []int64 {
	out := make([]int64, 0, len(assistant.ContextLimits))
	for _, n := range assistant.ContextLimits {
		if n <= m.ContextWindow {
			out = append(out, n)
		}
	}
	return out
}

func Composer(ctx context.Context, in ComposerState) ComposerView {
	effort, thinking, limit := in.Effort, in.Thinking, in.ContextLimit
	efforts, limits, known := []EffortOption{}, assistant.ContextLimits, false
	for _, e := range pricing.Efforts {
		efforts = append(efforts, EffortOption{Value: e})
	}
	thinkingOptions := []pricing.Thinking{}
	cliff := false
	if m, err := pricing.Lookup(in.Model); err == nil {
		known, limits = true, contextLimitsOf(m)
		effort = threads.SnapEffort(m, effort)
		if !m.ValidThinking(thinking, effort) {
			thinking = m.DefaultThinking()
		}
		thinkingOptions = m.Thinking
		efforts = efforts[:0]
		for _, e := range m.Efforts {
			efforts = append(efforts, EffortOption{Value: e, Disabled: !m.ValidThinking(thinking, e)})
		}
		if limit == 0 {
			limit = assistant.DefaultContextLimit
		}
		limit = threads.SnapContextLimit(m, limit)
		cliff = m.ID == pricing.HaikuID && limit > pricing.TierThreshold
	}
	return ComposerView{
		Models: pricing.Models(), Model: in.Model, Known: known, Efforts: efforts, Effort: effort,
		ContextLimits: limits, Context: limit, Thinking: thinking, ThinkingOptions: thinkingOptions,
		EffortFixed: threads.EffortFixed(thinking), WebSearch: in.WebSearch, CostCap: in.CostCap,
		Cliff: cliff, Coding: module.IsEnabled(ctx, "sandbox"), Live: in.Live,
	}
}

func DefaultComposer(ctx context.Context, d threads.Options) ComposerView {
	return Composer(ctx, ComposerState{Model: d.Model, Effort: d.Effort, Thinking: d.Thinking, ContextLimit: d.ContextLimit,
		WebSearch: d.WebSearch, CostCap: d.CostCapUSD})
}

func (in ComposerInput) state() (ComposerState, error) {
	model, err := threads.CleanModel(in.Model)
	if err != nil {
		return ComposerState{}, invalid("model", err.Error(), err)
	}
	out := ComposerState{Model: model.ID, Effort: in.Effort, Thinking: in.Thinking, WebSearch: in.WebSearch}
	out.ContextLimit, _ = threads.CleanContextLimit(in.ContextLimit)
	if in.Reset {
		out.Effort, out.Thinking, out.ContextLimit = pricing.Medium, "", assistant.DefaultContextLimit
	}
	if raw := strings.TrimSpace(in.CostCap); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			out.CostCap = &value
		}
	}
	return out, nil
}

func (s *Service) Configuration(ctx context.Context, in ConfigurationInput) (application.Read[ModelConfiguration], error) {
	var requested *ComposerState
	if in.Composer != nil {
		chosen, err := in.Composer.state()
		if err != nil {
			return application.Read[ModelConfiguration]{}, err
		}
		requested = &chosen
	}
	var out ModelConfiguration
	rev, err := s.snapshot(ctx, string(ConfigurationName), []string{state.Settings}, in, func(q db.Querier) error {
		defaults, err := threads.Defaults(ctx, q)
		if err != nil {
			return err
		}
		out = ModelConfiguration{Models: pricing.Models(), Defaults: defaults, Efforts: pricing.Efforts, ContextLimits: assistant.ContextLimits}
		if requested != nil {
			out.Composer = Composer(ctx, *requested)
		} else {
			out.Composer = DefaultComposer(ctx, defaults)
		}
		return nil
	})
	return application.Read[ModelConfiguration]{Revision: rev, Data: out}, err
}
