package loop

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/timex"
)

func MaxTokensFor(effort string) int64 {
	if effort == pricing.XHigh || effort == pricing.Max {
		return MaxTokensDeep
	}
	return MaxTokens
}

func TimeStamp(startedAt float64, loc *time.Location) string {
	sec, us := timex.SplitTimestamp(startedAt)
	return "Current date and time: " + time.Unix(sec, us*1000).In(loc).Format("Monday 2006-01-02 15:04 MST")
}

func cacheControlDict(cc prompts.CacheControl) map[string]any {
	d := map[string]any{"type": cc.Type}
	if cc.TTL != "" {
		d["ttl"] = cc.TTL
	}
	return d
}

type request struct {
	params      anthropic.Params
	cc          prompts.CacheControl
	sig         string
	fixedTokens int64
}

func (r *Runner) systemBlocks(ctx context.Context, q db.Querier, thread threads.Thread, cc prompts.CacheControl, extra string) ([]prompts.Block, error) {
	var p prompts.Parts
	var err error
	if p.System, err = prompts.Text(ctx, q, prompts.KeySystem); err != nil {
		return nil, err
	}
	if r.Profile != nil {
		if p.Profile, err = r.Profile(ctx, q); err != nil {
			return nil, err
		}
	}
	p.Extra = extra
	project, err := projects.SystemBlock(ctx, q, thread)
	if err != nil {
		return nil, err
	}
	if project != nil {
		p.Project = *project
	}
	return prompts.SystemBlocks(p, cc), nil
}

func (r *Runner) request(ctx context.Context, q db.Querier, thread threads.Thread, kind tool.Kind, model, effort string, set registry.Set,
	cc prompts.CacheControl, extra string, webSearchMaxUses int, dynamicFiltering bool) (request, error) {
	system, err := r.systemBlocks(ctx, q, thread, cc, extra)
	if err != nil {
		return request{}, err
	}
	cacheControl, err := cc.JSON()
	if err != nil {
		return request{}, err
	}
	toolParams, err := set.Params(webSearchMaxUses, cacheControl, dynamicFiltering)
	if err != nil {
		return request{}, err
	}
	sig, err := prompts.Signature(system, toolParams)
	if err != nil {
		return request{}, err
	}
	systemJSON, err := json.Marshal(system)
	if err != nil {
		return request{}, err
	}
	fixed, err := fixedPart(systemJSON, toolParams)
	if err != nil {
		return request{}, err
	}
	thinking := &anthropic.Thinking{Type: "adaptive", Display: "summarized"}
	if thread.Thinking == "disabled" {
		thinking = nil
	}
	return request{
		params: anthropic.Params{
			Model:        model,
			MaxTokens:    MaxTokensFor(effort),
			Thinking:     thinking,
			OutputConfig: &anthropic.OutputConfig{Effort: effort},
			System:       systemJSON,
			Tools:        toolParams,
		},
		cc:          cc,
		sig:         sig,
		fixedTokens: actx.EstimateValue(fixed),
	}, nil
}

func fixedPart(system json.RawMessage, tools []json.RawMessage) (map[string]any, error) {
	sys, err := jsonx.Loads(system)
	if err != nil {
		return nil, err
	}
	fixed := map[string]any{"system": sys}
	if len(tools) == 0 {
		return fixed, nil
	}
	list := make([]any, len(tools))
	for i, raw := range tools {
		if list[i], err = jsonx.Loads(raw); err != nil {
			return nil, err
		}
	}
	fixed["tools"] = list
	return fixed, nil
}

func withProjectFiles(ctx context.Context, q db.Querier, thread threads.Thread, active []actx.Message) ([]actx.Message, error) {
	if thread.ProjectID == nil || *thread.ProjectID == 0 {
		return active, nil
	}
	head, err := files.ProjectBlocks(ctx, q, *thread.ProjectID)
	if err != nil || head == nil {
		return active, err
	}
	if len(active) > 0 && active[0].Role == journal.User {
		first := active[0]
		first.Content = append(slices.Clone(head), first.Content...)
		return append([]actx.Message{first}, active[1:]...), nil
	}
	return append([]actx.Message{{Role: journal.User, Content: head}}, active...), nil
}

type anchor struct {
	tokens int64
	seq    int64
}

func messageTokens(m actx.Message) (int64, error) {
	shown, err := actx.EstimateContent(m.Content)
	if err != nil {
		return 0, err
	}
	hidden, err := files.HiddenTokens(m.Content)
	return shown + hidden, err
}

func (req request) estimate(active []actx.Message, a *anchor) (int64, error) {
	total := req.fixedTokens
	if a != nil {
		total = a.tokens
	}
	for _, m := range active {
		if a != nil && (m.Seq == nil || *m.Seq <= a.seq) {
			continue
		}
		n, err := messageTokens(m)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func (req request) buildMessages(ctx context.Context, q db.Querier, scope files.Scope, messages []actx.Message) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(messages))
	last := len(messages) - 1
	for i, m := range messages {
		content := m.Content
		if i == last && m.Role == journal.User && len(content) > 0 {
			content = slices.Clone(content)
			end := len(content) - 1
			marked, err := actx.SetField(content[end], "cache_control", cacheControlDict(req.cc))
			if err != nil {
				return nil, err
			}
			content[end] = marked
		}
		expanded, err := files.Expand(ctx, q, scope, content)
		if err != nil {
			return nil, err
		}
		out[i] = actx.Message{Role: m.Role, Content: expanded}.Wire()
	}
	return out, nil
}
