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
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

// MaxTokensFor is max_tokens_for: thinking counts toward max_tokens, and xhigh and max think at length.
func MaxTokensFor(effort string) int64 {
	if effort == threads.XHigh || effort == threads.Max {
		return MaxTokensDeep
	}
	return MaxTokens
}

// TimeStamp is time_stamp: the date line each operator message carries, in the ATRIUM_TZ zone.
func TimeStamp(startedAt float64, loc *time.Location) string {
	sec, us := py.SplitTimestamp(startedAt)
	return "Current date and time: " + time.Unix(sec, us*1000).In(loc).Format("Monday 2006-01-02 15:04 MST")
}

func cacheControlDict(cc prompts.CacheControl) py.Dict {
	d := py.Dict{{Key: "type", Value: cc.Type}}
	if cc.TTL != "" {
		d = append(d, py.Pair{Key: "ttl", Value: cc.TTL})
	}
	return d
}

// request is request_params without the messages, with what the loop derives from it once per run: the
// system signature and the token estimate of the fixed part.
type request struct {
	params      anthropic.Params
	cc          prompts.CacheControl
	sig         string
	fixedTokens int64
}

func (r *Runner) systemBlocks(ctx context.Context, q db.Querier, thread threads.Thread, cc prompts.CacheControl, extra string) ([]prompts.Block, error) {
	var p prompts.Parts
	var err error
	if r.Profile != nil {
		if p.Profile, err = r.Profile(ctx, q); err != nil {
			return nil, err
		}
	}
	if r.SkillsIndex != nil {
		if p.SkillsIndex, err = r.SkillsIndex(ctx, q); err != nil {
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

func (r *Runner) request(ctx context.Context, q db.Querier, thread threads.Thread, kind tool.Kind, effort string, tools []tool.Tool,
	cc prompts.CacheControl, extra string, allow []string, limits actx.Limits) (request, error) {
	system, err := r.systemBlocks(ctx, q, thread, cc, extra)
	if err != nil {
		return request{}, err
	}
	toolParams, err := registry.RequestTools(tools, kind, allow, cc.JSON())
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
	return request{
		params: anthropic.Params{
			Model:             Model,
			MaxTokens:         MaxTokensFor(effort),
			Thinking:          &anthropic.Thinking{Type: "adaptive", Display: "summarized"},
			OutputConfig:      &anthropic.OutputConfig{Effort: effort},
			System:            systemJSON,
			ContextManagement: actx.ContextManagement(limits),
			Tools:             toolParams,
			Betas:             actx.Betas(),
		},
		cc:          cc,
		sig:         sig,
		fixedTokens: actx.EstimateValue(fixed),
	}, nil
}

// fixedPart is {system, tools} of the params as estimate reads them (tools only when the run has any).
func fixedPart(system json.RawMessage, tools []json.RawMessage) (py.Dict, error) {
	sys, err := py.JSONLoads(system)
	if err != nil {
		return nil, err
	}
	fixed := py.Dict{{Key: "system", Value: sys}}
	if len(tools) == 0 {
		return fixed, nil
	}
	list := make([]any, len(tools))
	for i, raw := range tools {
		if list[i], err = py.JSONLoads(raw); err != nil {
			return nil, err
		}
	}
	return append(fixed, py.Pair{Key: "tools", Value: list}), nil
}

// stampMessages is stamp_messages: each operator message carries the date stamp of its own run, so earlier
// turns never change.
func stampMessages(active []actx.Message, runs []journal.Run, loc *time.Location) []actx.Message {
	started := make(map[int64]float64, len(runs))
	for _, r := range runs {
		started[r.ID] = r.StartedAt
	}
	out := make([]actx.Message, len(active))
	for i, m := range active {
		out[i] = m
		if m.Role != journal.User || m.RunID == nil || !journal.HasText(m.Content) {
			continue
		}
		at, ok := started[*m.RunID]
		if !ok {
			continue
		}
		out[i].Content = append(slices.Clone(m.Content), actx.TextBlock(TimeStamp(at, loc)))
	}
	return out
}

// withProjectFiles is files.with_project_files: the project's files open the first user turn.
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

// anchor is the last response's conversation size and the seq it was journaled at.
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

// estimate is estimate: the last response's usage plus everything appended since, else chars/4 of it all.
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

// buildMessages is build_messages then files.expand: the request's messages, the last user block carrying the
// history breakpoint, attachments expanded into their content blocks.
func (req request) buildMessages(ctx context.Context, q db.Querier, messages []actx.Message) ([]json.RawMessage, error) {
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
		expanded, err := files.Expand(ctx, q, content)
		if err != nil {
			return nil, err
		}
		out[i] = actx.Message{Role: m.Role, Content: expanded}.Wire()
	}
	return out, nil
}
