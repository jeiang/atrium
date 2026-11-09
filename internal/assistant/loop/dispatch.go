package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/assistant/block"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/tool"
)

type ToolUse = block.Use

func toolUses(blocks []json.RawMessage) ([]ToolUse, error) {
	return block.ToolUses(blocks)
}

func toolResult(toolUseID, content string, isError bool) json.RawMessage {
	block := map[string]any{"type": actx.ToolResult, "tool_use_id": toolUseID, "content": content}
	if isError {
		block["is_error"] = true
	}
	return json.RawMessage(jsonx.Dumps(block, jsonx.Opts{Compact: true, NoEnsureASCII: true}))
}

func replayClass(t *tool.Tool) string {
	if t == nil || t.Effect == tool.Read {
		return journal.Safe
	}
	return journal.Write
}

func className(err error) string {
	var raised *tool.Raised
	if errors.As(err, &raised) {
		return raised.Error()
	}
	var te tool.Error
	if errors.As(err, &te) {
		return string(te)
	}
	var nc config.NotConfigured
	if errors.As(err, &nc) {
		return string(nc)
	}
	var unavailable *httpx.ServiceUnavailable
	if errors.As(err, &unavailable) {
		return "service unavailable"
	}
	return err.Error()
}

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
	d         *db.DB
	tc        *tool.Context
	byName    map[string]tool.Tool
	withdrawn map[string]struct{}
	now       func() float64
	tuning    actx.Tuning
	guard     func(ctx context.Context) error
	lost      context.Context
	abort     context.CancelFunc
}

func (x *dispatcher) lookup(name string) *tool.Tool {
	t, ok := x.byName[name]
	if !ok {
		return nil
	}
	return &t
}

func (x *dispatcher) invoke(ctx context.Context, t *tool.Tool, use ToolUse) (errText *string, value any, err error) {
	fail := func(text string) (*string, any, error) { return new(text), nil, nil }
	if t == nil {
		if _, ok := x.withdrawn[use.Name]; ok {
			return fail(use.Name + " is no longer available in this thread: it has read private data or web content")
		}
		return fail("unknown tool " + use.Name)
	}
	_, args, err := use.Args()
	if err != nil {
		return nil, nil, err
	}
	value, err = x.call(ctx, t, tool.Args(args))
	if err == nil {
		returned := tool.Marks(0)
		if marked, ok := value.(tool.Marked); ok {
			value, returned = marked.Value, marked.Marks
		}
		if err := threads.AddMarks(context.WithoutCancel(ctx), x.d.Write, x.tc.ThreadID, x.now(), t.MarksIn(x.tc.Kind)|returned); err != nil {
			return nil, nil, err
		}
		return nil, value, nil
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if x.lost.Err() != nil {
		return nil, nil, LeaseLost(fmt.Sprintf("run %d was taken over by another process", x.tc.RunID))
	}
	var park *tool.Park
	if errors.As(err, &park) {
		return nil, nil, park
	}
	if text, ok := toolErrorText(err); ok {
		return fail(text)
	}
	log.Error(fmt.Sprintf("tool %s failed", use.Name), logx.RunID, x.tc.RunID, logx.Err, err)
	return fail(fmt.Sprintf("%s failed: %s", use.Name, className(err)))
}

func (x *dispatcher) call(ctx context.Context, t *tool.Tool, args tool.Args) (value any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(x.lost, cancel)()
	return t.Handler(ctx, x.tc, args)
}

func (x *dispatcher) finish(ctx context.Context, callID int64, use ToolUse, errText *string, value any) (json.RawMessage, error) {
	ledgerCtx := context.WithoutCancel(ctx)
	if errText != nil {
		if err := journal.FinishToolCall(ledgerCtx, x.d.Write, callID, journal.Error, x.now(), errText, nil); err != nil {
			return nil, err
		}
		if err := x.tc.Progress.Event(ctx, progress.ToolDone, map[string]any{"id": use.ID, "ok": false, "preview": progress.PreviewOf(*errText)}); err != nil {
			return nil, err
		}
		return toolResult(use.ID, *errText, true), nil
	}
	content, resultID, err := actx.Ingest(ledgerCtx, x.d.Write, x.tc.RunID, callID, use.Name, value, x.now(), x.tuning)
	if err != nil {
		return nil, err
	}
	if err := journal.FinishToolCall(ledgerCtx, x.d.Write, callID, journal.OK, x.now(), nil, resultID); err != nil {
		return nil, err
	}
	if err := x.tc.Progress.Event(ctx, progress.ToolDone, map[string]any{"id": use.ID, "ok": true, "preview": progress.PreviewOf(content)}); err != nil {
		return nil, err
	}
	return toolResult(use.ID, content, false), nil
}

func (x *dispatcher) perform(ctx context.Context, t *tool.Tool, callID int64, use ToolUse) (json.RawMessage, error) {
	name, effect := "unknown", "unknown"
	if t != nil {
		name, effect = t.Name, string(t.Effect)
	}
	ctx, span := obs.Start(ctx, "tool.call", trace.WithAttributes(
		obs.String(obs.ToolName, name),
		obs.String(obs.ToolEffect, effect),
	))
	started := time.Now()
	errText, value, err := x.invoke(ctx, t, use)
	var out json.RawMessage
	if err == nil {
		out, err = x.finish(ctx, callID, use, errText, value)
	}
	duration := time.Since(started)
	ok := err == nil && errText == nil
	span.SetAttributes(obs.Bool(obs.ToolOK, ok), obs.Int64(obs.ToolDurationMS, duration.Milliseconds()))
	if !ok {
		class := spanError(err)
		if class == "" {
			class = "tool_error"
		}
		obs.Fail(span, class)
		span.SetAttributes(obs.String(obs.ErrorType, class))
	}
	obs.ToolCalls.Add(1, name, strconv.FormatBool(ok))
	obs.ToolDuration.Observe(duration.Seconds(), name, strconv.FormatBool(ok))
	span.End()
	return out, err
}

func (x *dispatcher) toolStart(ctx context.Context, use ToolUse, args json.RawMessage) error {
	short, err := progress.ShortArgs(args)
	if err != nil {
		return err
	}
	return x.tc.Progress.Event(ctx, progress.ToolStart, map[string]any{"id": use.ID, "name": use.Name, "args": short})
}

func (x *dispatcher) startCall(ctx context.Context, use ToolUse) (int64, error) {
	if err := x.guard(ctx); err != nil {
		return 0, err
	}
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

func (x *dispatcher) executeAll(ctx context.Context, uses []ToolUse) ([]json.RawMessage, error) {
	results := make([]json.RawMessage, len(uses))
	for i := 0; i < len(uses); {
		t := x.lookup(uses[i].Name)
		if t == nil || !t.Parallel {
			r, err := x.execute(ctx, uses[i])
			if err != nil {
				return nil, err
			}
			results[i] = r
			i++
			continue
		}
		j := i
		for j < len(uses) {
			n := x.lookup(uses[j].Name)
			if n == nil || !n.Parallel {
				break
			}
			j++
		}
		if err := x.executeParallel(ctx, uses[i:j], results[i:j]); err != nil {
			return nil, err
		}
		i = j
	}
	return results, nil
}

func (x *dispatcher) executeParallel(ctx context.Context, uses []ToolUse, out []json.RawMessage) error {
	calls := make([]int64, len(uses))
	for i, u := range uses {
		id, err := x.startCall(ctx, u)
		if err != nil {
			return err
		}
		calls[i] = id
	}
	errs := make([]error, len(uses))
	var wg sync.WaitGroup
	for i := range uses {
		wg.Go(func() {
			out[i], errs[i] = x.perform(ctx, x.lookup(uses[i].Name), calls[i], uses[i])
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (x *dispatcher) settle(ctx context.Context, use ToolUse, call *journal.ToolCall) (json.RawMessage, error) {
	if err := x.guard(ctx); err != nil {
		return nil, err
	}
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
