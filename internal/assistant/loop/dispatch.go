package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"

	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

// ToolUse is a tool_use block of a response.
type ToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Args is `use.get("input") or {}`: the input as the journal stores it and as the handler gets it.
func (u ToolUse) Args() (json.RawMessage, any, error) {
	if len(u.Input) == 0 {
		return json.RawMessage(`{}`), py.Dict{}, nil
	}
	v, err := py.JSONLoads(u.Input)
	if err != nil {
		return nil, nil, fmt.Errorf("loop: tool %s input: %w", u.Name, err)
	}
	if !py.Truthy(v) {
		return json.RawMessage(`{}`), py.Dict{}, nil
	}
	return u.Input, v, nil
}

// Outcome is one call of a parallel batch: Value when OK, else Error is the call's error text.
type Outcome struct {
	OK    bool
	Value any
	Error string
}

// Batch runs several calls of one tool concurrently (research.run_batch); it never touches the journal.
type Batch func(ctx context.Context, tc *tool.Context, uses []ToolUse) (map[string]Outcome, error)

func blockType(raw json.RawMessage) string {
	var b struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &b)
	return b.Type
}

func toolUses(blocks []json.RawMessage) ([]ToolUse, error) {
	var uses []ToolUse
	for _, raw := range blocks {
		if blockType(raw) != actx.ToolUse {
			continue
		}
		var u ToolUse
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, fmt.Errorf("loop: tool_use block: %w", err)
		}
		uses = append(uses, u)
	}
	return uses, nil
}

// toolResult is tool_result.
func toolResult(toolUseID, content string, isError bool) json.RawMessage {
	block := py.Dict{{Key: "type", Value: actx.ToolResult}, {Key: "tool_use_id", Value: toolUseID}, {Key: "content", Value: content}}
	if isError {
		block = append(block, py.Pair{Key: "is_error", Value: true})
	}
	return json.RawMessage(py.JSONDumps(block, journalJSON))
}

// replayClass is replay_class: an unknown tool or a read is safe to run again on resume.
func replayClass(t *tool.Tool) string {
	if t == nil || t.Effect == tool.Read {
		return journal.Safe
	}
	return journal.Write
}

// className is type(e).__name__ of an error a tool raised.
func className(err error) string {
	var raised *tool.Raised
	if errors.As(err, &raised) {
		return raised.Class
	}
	var classed py.Classed
	if errors.As(err, &classed) {
		return classed.PyClass()
	}
	for _, sentinel := range []error{httpx.ErrValue, httpx.ErrKey, httpx.ErrIndex, httpx.ErrType} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	var unavailable *httpx.ServiceUnavailable
	if errors.As(err, &unavailable) {
		return "ServiceUnavailable"
	}
	return "RuntimeError"
}

// toolErrorText is `str(e) or type(e).__name__` for a ToolError (NotConfigured included).
func toolErrorText(err error) (string, bool) {
	if text, ok := tool.Message(err); ok {
		return text, true
	}
	var nc config.NotConfigured
	if errors.As(err, &nc) {
		if nc == "" {
			return "NotConfigured", true
		}
		return string(nc), true
	}
	return "", false
}

type dispatcher struct {
	d      *db.DB
	tc     *tool.Context
	byName map[string]tool.Tool
	now    func() float64
	batch  Batch
}

func (x *dispatcher) lookup(name string) *tool.Tool {
	t, ok := x.byName[name]
	if !ok {
		return nil
	}
	return &t
}

// invoke is invoke: the handler's value, or the error text the model sees. A tool the run does not offer is
// refused here, whatever the model named.
func (x *dispatcher) invoke(ctx context.Context, t *tool.Tool, use ToolUse) (errText *string, value any, err error) {
	fail := func(text string) (*string, any, error) { return new(text), nil, nil }
	if t == nil {
		return fail("unknown tool " + use.Name)
	}
	_, args, err := use.Args()
	if err != nil {
		return nil, nil, err
	}
	dict, ok := args.(py.Dict)
	if !ok {
		return fail("tool input must be an object")
	}
	value, err = x.call(ctx, t, dict)
	if err == nil {
		return nil, value, nil
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if text, ok := toolErrorText(err); ok {
		return fail(text)
	}
	log.Error(fmt.Sprintf("tool %s failed", use.Name), logx.RunID, x.tc.RunID, logx.Err, err)
	return fail(fmt.Sprintf("%s failed: %s", use.Name, className(err)))
}

func (x *dispatcher) call(ctx context.Context, t *tool.Tool, args py.Dict) (value any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return t.Handler(ctx, x.tc, args)
}

// finish is finish: it records the outcome and returns the tool_result block.
func (x *dispatcher) finish(ctx context.Context, callID int64, use ToolUse, errText *string, value any) (json.RawMessage, error) {
	if errText != nil {
		if err := journal.FinishToolCall(ctx, x.d.Write, callID, journal.Error, x.now(), errText, nil); err != nil {
			return nil, err
		}
		if err := x.tc.Progress.Event(ctx, progress.ToolDone, py.Dict{{Key: "id", Value: use.ID}, {Key: "ok", Value: false}, {Key: "preview", Value: progress.PreviewOf(*errText)}}); err != nil {
			return nil, err
		}
		return toolResult(use.ID, *errText, true), nil
	}
	content, resultID, err := actx.Ingest(ctx, x.d.Write, x.tc.RunID, callID, use.Name, value, x.now())
	if err != nil {
		return nil, err
	}
	if err := journal.FinishToolCall(ctx, x.d.Write, callID, journal.OK, x.now(), nil, resultID); err != nil {
		return nil, err
	}
	if err := x.tc.Progress.Event(ctx, progress.ToolDone, py.Dict{{Key: "id", Value: use.ID}, {Key: "ok", Value: true}, {Key: "preview", Value: progress.PreviewOf(content)}}); err != nil {
		return nil, err
	}
	return toolResult(use.ID, content, false), nil
}

func (x *dispatcher) perform(ctx context.Context, t *tool.Tool, callID int64, use ToolUse) (json.RawMessage, error) {
	errText, value, err := x.invoke(ctx, t, use)
	if err != nil {
		return nil, err
	}
	return x.finish(ctx, callID, use, errText, value)
}

func (x *dispatcher) toolStart(ctx context.Context, use ToolUse, args json.RawMessage) error {
	short, err := progress.ShortArgs(args)
	if err != nil {
		return err
	}
	return x.tc.Progress.Event(ctx, progress.ToolStart, py.Dict{{Key: "id", Value: use.ID}, {Key: "name", Value: use.Name}, {Key: "args", Value: short}})
}

// startCall is start_call: the call is journaled before the tool runs.
func (x *dispatcher) startCall(ctx context.Context, use ToolUse) (int64, error) {
	args, _, err := use.Args()
	if err != nil {
		return 0, err
	}
	if err := x.toolStart(ctx, use, args); err != nil {
		return 0, err
	}
	return journal.StartToolCall(ctx, x.d.Write, x.tc.RunID, use.ID, use.Name, args, replayClass(x.lookup(use.Name)), x.now())
}

func (x *dispatcher) execute(ctx context.Context, use ToolUse) (json.RawMessage, error) {
	callID, err := x.startCall(ctx, use)
	if err != nil {
		return nil, err
	}
	return x.perform(ctx, x.lookup(use.Name), callID, use)
}

// executeAll is execute_all: several research calls in one response run as a parallel batch; journal
// writes stay on this goroutine.
func (x *dispatcher) executeAll(ctx context.Context, uses []ToolUse) ([]json.RawMessage, error) {
	var batch []ToolUse
	if _, offered := x.byName[Research]; offered && x.batch != nil {
		for _, u := range uses {
			if u.Name == Research {
				batch = append(batch, u)
			}
		}
	}
	done := map[string]json.RawMessage{}
	if len(batch) >= 2 {
		calls := make(map[string]int64, len(batch))
		for _, u := range batch {
			id, err := x.startCall(ctx, u)
			if err != nil {
				return nil, err
			}
			calls[u.ID] = id
		}
		outcomes, err := x.batch(ctx, x.tc, batch)
		if err != nil {
			return nil, err
		}
		for _, u := range batch {
			o := outcomes[u.ID]
			var errText *string
			if !o.OK {
				errText = &o.Error
			}
			result, err := x.finish(ctx, calls[u.ID], u, errText, o.Value)
			if err != nil {
				return nil, err
			}
			done[u.ID] = result
		}
	}
	results := make([]json.RawMessage, len(uses))
	for i, u := range uses {
		if r, ok := done[u.ID]; ok {
			results[i] = r
			continue
		}
		r, err := x.execute(ctx, u)
		if err != nil {
			return nil, err
		}
		results[i] = r
	}
	return results, nil
}

// settle is settle: a pending call of an interrupted run. A safe call runs again; a write that may have
// taken effect is answered with an instruction to check before retrying.
func (x *dispatcher) settle(ctx context.Context, use ToolUse, call *journal.ToolCall) (json.RawMessage, error) {
	if call == nil {
		return x.execute(ctx, use)
	}
	if call.ReplayClass == journal.Safe {
		if _, err := x.d.Write.ExecContext(ctx,
			"UPDATE tool_calls SET state = ?, error = NULL, result_id = NULL, finished_at = NULL, started_at = ? WHERE id = ?",
			journal.Started, x.now(), call.ID); err != nil {
			return nil, err
		}
		args, _, err := use.Args()
		if err != nil {
			return nil, err
		}
		if err := x.toolStart(ctx, use, args); err != nil {
			return nil, err
		}
		return x.perform(ctx, x.lookup(use.Name), call.ID, use)
	}
	switch call.State {
	case journal.Error:
		text := "failed"
		if call.Error != nil && *call.Error != "" {
			text = *call.Error
		}
		return toolResult(use.ID, text, true), nil
	case journal.OK:
		return toolResult(use.ID, CompletedUncaptured, true), nil
	}
	if err := journal.FinishToolCall(ctx, x.d.Write, call.ID, journal.Unknown, x.now(), new(Verify), nil); err != nil {
		return nil, err
	}
	return toolResult(use.ID, Verify, true), nil
}
