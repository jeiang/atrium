package context

import (
	gocontext "context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func callsByUse(ctx gocontext.Context, q db.Querier, threadID int64) (map[string]journal.ToolCall, error) {
	all, err := journal.ToolCallsForThread(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]journal.ToolCall, len(all))
	for _, c := range all {
		if have, ok := out[c.ToolUseID]; !ok || c.ID > have.ID {
			out[c.ToolUseID] = c
		}
	}
	return out, nil
}

type item struct {
	ID       string
	Seq      int64
	Tool     string
	Tokens   int64
	State    State
	ResultID *int64
}

func resultContent(raw json.RawMessage) (any, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, fmt.Errorf("context: content block: %w", err)
	}
	d, ok := v.(py.Dict)
	if !ok {
		return nil, fmt.Errorf("context: content block is %s, not an object", py.TypeName(v))
	}
	if c, ok := d.Get("content"); ok {
		return c, nil
	}
	return "", nil
}

func nonZero(p *int64) *int64 {
	if p == nil || *p == 0 {
		return nil
	}
	return p
}

func items(ctx gocontext.Context, q db.Querier, threadID int64) ([]item, error) {
	p, err := getProjection(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	ledger, err := loadLedger(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	folds := ledger.withState(StateFolded)
	starts := map[int64]bool{}
	for _, f := range folds {
		starts[f.Fold.From] = true
	}
	calls, err := callsByUse(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	tail, err := tailOf(ctx, q, threadID, p)
	if err != nil {
		return nil, err
	}
	var found []item
	for _, m := range tail {
		if m.Role != journal.User || inFold(folds, m.Seq) || starts[m.Seq] {
			continue
		}
		for _, raw := range m.Content {
			h := headOf(raw)
			if h.Type != ToolResult {
				continue
			}
			call, hasCall := calls[h.ToolUseID]
			row, hasRow := ledger.Get(h.ToolUseID)
			it := item{ID: h.ToolUseID, Seq: m.Seq, Tool: "?", State: StateActive}
			if hasRow {
				it.State = row.State
				it.ResultID = nonZero(row.ResultID)
			}
			if hasCall {
				it.Tool = call.Name
				if it.ResultID == nil {
					it.ResultID = nonZero(call.ResultID)
				}
			}
			content, err := resultContent(raw)
			if err != nil {
				return nil, err
			}
			it.Tokens = EstimateTokens(serialize(content))
			found = append(found, it)
		}
	}
	return found, nil
}

func estimateMessages(messages []Message) (int64, error) {
	var sum int64
	for _, m := range messages {
		n, err := EstimateContent(m.Content)
		if err != nil {
			return 0, err
		}
		sum += n
	}
	return sum, nil
}
