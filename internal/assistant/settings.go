package assistant

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/settings"
)

const module = "assistant"

const (
	KeyBudgetUnattended = "budget_unattended_share"
	KeyBudgetChat       = "budget_chat_share"

	KeyDefaultModel        = "default_model"
	KeyDefaultEffort       = "default_effort"
	KeyDefaultContextLimit = "default_context_limit"
	KeyEffortCron          = "agent_effort_cron"
	KeyEffortNightly       = "agent_effort_nightly"
	KeyEffortSubagent      = "agent_effort_subagent"

	KeySubagentMaxCostUSD = "agent_subagent_max_cost_usd"

	KeyDreamingMaxCost = "dreaming_max_cost_usd"
	KeyDreamingMonthly = "dreaming_monthly_usd"
)

const DefaultContextLimit = 90_000

var ContextLimits = []int64{90_000, 200_000, 500_000, 1_000_000}

func contextLimitChoices() []string {
	out := make([]string, len(ContextLimits))
	for i, n := range ContextLimits {
		out[i] = strconv.FormatInt(n, 10)
	}
	return out
}

func Settings() []settings.Def {
	def := func(key string, kind settings.Type, surface settings.Surface, value string, check func(string) error) settings.Def {
		return settings.Value(surface, settings.Def{Key: key, Module: module, Type: kind, Default: value, Check: check})
	}
	adv := func(key string, kind settings.Type, value string, check func(string) error) settings.Def {
		return def(key, kind, settings.Advanced, value, check)
	}
	effort := func(key, value string) settings.Def {
		d := adv(key, settings.Enum, value, nil)
		d.Choices = pricing.Efforts
		return d
	}
	chatModel := def(KeyDefaultModel, settings.Enum, settings.UI, pricing.DefaultModel, nil)
	chatModel.Choices = pricing.IDs()
	chatEffort := def(KeyDefaultEffort, settings.Enum, settings.UI, pricing.XHigh, nil)
	chatEffort.Choices = pricing.Efforts
	contextLimit := def(KeyDefaultContextLimit, settings.Enum, settings.UI, strconv.FormatInt(DefaultContextLimit, 10), nil)
	contextLimit.Choices = contextLimitChoices()
	chat := adv(KeyBudgetChat, settings.Float, "1", settings.Within(0, 1))
	chat.Peers = Ordered([]string{KeyBudgetUnattended}, nil)
	chat = settings.Shown("Chat stops at this share of the budget", chat)
	return []settings.Def{
		chat,
		chatModel,
		chatEffort,
		contextLimit,
		settings.Shown("Scheduled run effort", effort(KeyEffortCron, "high")),
		settings.Shown("Nightly run effort", effort(KeyEffortNightly, "high")),
		settings.Shown("Research effort", effort(KeyEffortSubagent, "medium")),
		settings.Shown("Research run cost cap (USD)", adv(KeySubagentMaxCostUSD, settings.Float, "0.25", settings.Within(0, 2))),
		settings.Shown("Nightly run cost cap (USD)", adv(KeyDreamingMaxCost, settings.Float, "0.30", settings.Within(0, 10))),
		settings.Shown("Nightly monthly cap (USD)", adv(KeyDreamingMonthly, settings.Float, "3", settings.Within(0, 100))),
	}
}

func Ordered(below, above []string) func(string, func(string) (string, error)) error {
	return func(value string, peer func(key string) (string, error)) error {
		own, err := numx.ParseFloat(value)
		if err != nil {
			return settings.Invalid("must be a number")
		}
		for _, key := range below {
			text, err := peer(key)
			if err != nil {
				return err
			}
			if other, err := numx.ParseFloat(text); err == nil && own < other {
				return settings.Invalid(fmt.Sprintf("must be at least %s (%s)", key, text))
			}
		}
		for _, key := range above {
			text, err := peer(key)
			if err != nil {
				return err
			}
			if other, err := numx.ParseFloat(text); err == nil && own > other {
				return settings.Invalid(fmt.Sprintf("must be at most %s (%s)", key, text))
			}
		}
		return nil
	}
}

type Reader struct {
	ctx context.Context
	q   db.Querier
	err error
}

func Read(ctx context.Context, q db.Querier) *Reader { return &Reader{ctx: ctx, q: q} }

func (r *Reader) Err() error { return r.err }

func (r *Reader) resolve(key string) settings.Resolved {
	if r.err != nil {
		return settings.Resolved{}
	}
	found, err := settings.From(r.ctx).Resolve(r.ctx, r.q, key)
	r.err = err
	return found
}

func (r *Reader) Text(key string) string { return r.resolve(key).Value }

func (r *Reader) Int(key string) int {
	found := r.resolve(key)
	if r.err != nil {
		return 0
	}
	n, err := found.Int()
	r.err = err
	return int(n)
}

func (r *Reader) Float(key string) float64 {
	found := r.resolve(key)
	if r.err != nil {
		return 0
	}
	f, err := found.Float()
	r.err = err
	return f
}
