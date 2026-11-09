package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/block"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/tool"
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

func (r *Runner) systemBlocks(ctx context.Context, q db.Querier, thread threads.Thread, kind tool.Kind, cc prompts.CacheControl, extra string) ([]prompts.Block, error) {
	var p prompts.Parts
	var err error
	if p.System, err = prompts.Text(ctx, q, prompts.KeySystem); err != nil {
		return nil, err
	}
	p.Extra = extra
	if !registry.PromptCarriesPrivate(kind) {
		return prompts.SystemBlocks(p, cc), nil
	}
	if r.Profile != nil {
		if p.Profile, err = r.Profile(ctx, q); err != nil {
			return nil, err
		}
	}
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
	system, err := r.systemBlocks(ctx, q, thread, kind, cc, extra)
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

type history struct {
	from   int64
	prefix []json.RawMessage
}

func (s *state) history(ctx context.Context) (*history, error) {
	if s.hist != nil {
		return s.hist, nil
	}
	if err := memory.Rebuild(ctx, s.d.Write, s.thread.ID, s.now()); err != nil {
		return nil, err
	}
	messages, err := journal.MessagesOf(ctx, s.d.Read, s.thread.ID)
	if err != nil {
		return nil, err
	}
	budget, err := memory.VerbatimTokens(ctx, s.d.Read)
	if err != nil {
		return nil, err
	}
	from, err := verbatimFrom(messages, s.run.ID, min(budget, (s.contextLimit-s.req.fixedTokens)/4))
	if err != nil {
		return nil, err
	}
	lines, err := memory.Older(ctx, s.d.Read, s.thread.ID, from)
	if err != nil {
		return nil, err
	}
	var prefix []json.RawMessage
	if s.thread.ProjectID != nil && *s.thread.ProjectID != 0 {
		head, err := files.ProjectBlocks(ctx, s.d.Read, *s.thread.ProjectID)
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, head...)
	}
	if len(lines) > 0 {
		prefix = append(prefix, actx.TextBlock("Memory view of this thread's older history (message ranges and zoom ids):"))
		for _, line := range lines {
			prefix = append(prefix, actx.TextBlock(fmt.Sprintf("[%d..%d] zoom id %d n %d (%s)\n%s", line.From, line.To, line.From, 1<<line.Level, line.State, line.Text)))
		}
	}
	if n := len(prefix); n > 0 {
		if prefix[n-1], err = actx.SetField(prefix[n-1], "cache_control", cacheControlDict(s.req.cc)); err != nil {
			return nil, err
		}
	}
	s.hist = &history{from: from, prefix: prefix}
	return s.hist, nil
}

func verbatimFrom(messages []journal.Message, runID, budget int64) (int64, error) {
	n := len(messages)
	if n == 0 {
		return 0, nil
	}
	back := func(i int) (int, error) {
		for ; i > 0; i-- {
			if messages[i].Role != journal.User {
				continue
			}
			results, err := block.HasKind(messages[i].Content, block.ToolResult)
			if err != nil || !results {
				return i, err
			}
		}
		return 0, nil
	}
	start := n
	for i, m := range messages {
		if m.RunID != nil && *m.RunID == runID {
			start = i
			break
		}
	}
	var err error
	if start < n {
		if start, err = back(start); err != nil {
			return 0, err
		}
	}
	var spent int64
	for start > 0 {
		j, err := back(start - 1)
		if err != nil {
			return 0, err
		}
		for _, m := range messages[j:start] {
			t, err := messageTokens(actx.Message{Content: m.Content})
			if err != nil {
				return 0, err
			}
			spent += t
		}
		if spent > budget {
			break
		}
		start = j
	}
	if start < n {
		return messages[start].Seq, nil
	}
	return messages[n-1].Seq + 1, nil
}

func (s *state) active(ctx context.Context, loc *time.Location, h *history) ([]actx.Message, error) {
	messages, err := journal.MessagesOf(ctx, s.d.Read, s.thread.ID)
	if err != nil {
		return nil, err
	}
	runs, err := journal.RunsOf(ctx, s.d.Read, s.thread.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]journal.Run, len(runs))
	for _, r := range runs {
		byID[r.ID] = r
	}
	sum := sha256.New()
	feed(sum, "", []json.RawMessage{json.RawMessage(s.run.Model), json.RawMessage(s.run.Effort), json.RawMessage(s.thread.Thinking), json.RawMessage(s.req.sig)})
	feed(sum, journal.User, h.prefix)
	var out []actx.Message
	for _, m := range messages {
		if m.Seq < h.from {
			continue
		}
		content := m.Content
		run, known := journal.Run{}, m.RunID != nil
		if known {
			run, known = byID[*m.RunID]
		}
		switch m.Role {
		case journal.User:
			text, err := journal.HasText(content)
			if err != nil {
				return nil, err
			}
			if text && known {
				content = append(slices.Clone(content), actx.TextBlock(TimeStamp(run.StartedAt, loc)))
			}
		case journal.Assistant:
			if s.keepsThinking(m, hex.EncodeToString(sum.Sum(nil))) {
				break
			}
			if content, err = actx.StripThinking(content); err != nil {
				return nil, err
			}
			if len(content) == 0 {
				continue
			}
		}
		feed(sum, m.Role, content)
		seq := m.Seq
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = append(slices.Clone(out[n-1].Content), content...)
			out[n-1].Seq = &seq
			continue
		}
		out = append(out, actx.Message{Role: m.Role, Content: content, Seq: &seq, RunID: m.RunID})
	}
	s.sig = hex.EncodeToString(sum.Sum(nil))
	if len(h.prefix) == 0 {
		return out, nil
	}
	if len(out) > 0 && out[0].Role == journal.User {
		out[0].Content = append(slices.Clone(h.prefix), out[0].Content...)
		return out, nil
	}
	return append([]actx.Message{{Role: journal.User, Content: h.prefix}}, out...), nil
}

func feed(sum hash.Hash, role string, content []json.RawMessage) {
	for _, b := range content {
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", role, len(b))
		sum.Write(b)
	}
}

func (s *state) keepsThinking(m journal.Message, sig string) bool {
	return s.thread.Thinking != "disabled" && m.SystemSig != nil && *m.SystemSig == sig
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
		expanded, err = references.Expand(expanded)
		if err != nil {
			return nil, err
		}
		out[i] = actx.Message{Role: m.Role, Content: expanded}.Wire()
	}
	return out, nil
}
