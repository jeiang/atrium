package approval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/proposals"
)

type Payload struct {
	Args     json.RawMessage `json:"args"`
	ThreadID int64           `json:"thread_id"`
	RunID    int64           `json:"run_id"`
	RunKind  tool.Kind       `json:"run_kind"`
}

type Preview struct {
	Args tool.Args `json:"args"`
}

func Gate(t tool.Tool) tool.Tool {
	name, a := t.Name, t.Approval
	t.Handler = func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		return request(ctx, tc, name, a, args)
	}
	return t
}

func Title(a *tool.Approval, args tool.Args) string {
	if a.Detail == "" {
		return a.Label
	}
	detail, _ := args[a.Detail].(string)
	if detail == "" {
		return a.Label
	}
	return a.Label + ": " + tool.Clip(detail, 120)
}

func request(ctx context.Context, tc *tool.Context, name string, a *tool.Approval, args tool.Args) (any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s", name, tc.RunID, raw))
	payload := Payload{Args: raw, ThreadID: tc.ThreadID, RunID: tc.RunID, RunKind: tc.Kind}
	id, created, err := proposals.Create(ctx, tc.DB, name, nil, Title(a, args), payload, Preview{Args: args}, hex.EncodeToString(sum[:]), tc.Now())
	if err != nil {
		return nil, err
	}
	if !created {
		return map[string]any{"proposed": false, "reason": "this request is already waiting for the operator"}, nil
	}
	return map[string]any{"proposed": true, "proposal_id": id, "status": "pending"}, nil
}

type Executor struct {
	DB      *db.DB
	Env     config.Env
	Now     func() float64
	Undoers *writes.Registry
	Tools   []tool.Tool
}

func (e *Executor) Register(r *proposals.Registry) {
	for _, t := range e.Tools {
		if t.Approval == nil {
			continue
		}
		r.Executors[t.Name] = e.execute(t)
		r.AddKind(proposals.Kind{Name: t.Name, Module: t.Approval.Module, Label: t.Approval.Label, Icon: t.Approval.Icon, Approval: true})
	}
}

func (e *Executor) now() float64 {
	if e.Now != nil {
		return e.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (e *Executor) execute(t tool.Tool) proposals.Executor {
	return func(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
		var payload Payload
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(payload.Args))
		dec.UseNumber()
		args := tool.Args{}
		if err := dec.Decode(&args); err != nil {
			return nil, err
		}
		action := map[string]any{"tool": t.Name}
		start := e.now()
		held, err := proposals.BeginAction(ctx, e.DB.Write, p.ID, action, start)
		if err != nil {
			return nil, err
		}
		tc := &tool.Context{
			DB: e.DB, Env: e.Env, Kind: payload.RunKind, ThreadID: payload.ThreadID, RunID: payload.RunID,
			AllowedURLs: map[string]struct{}{}, Now: e.now, Undoers: e.Undoers,
		}
		value, err := t.Approval.Apply(ctx, tc, args)
		if err != nil {
			text := err.Error()
			var unknown bool
			if qerr := e.DB.Write.QueryRowContext(context.WithoutCancel(ctx),
				"SELECT EXISTS (SELECT 1 FROM agent_writes WHERE run_id = ? AND tool = ? AND state = 'unknown' AND ts >= ?)",
				payload.RunID, t.Name, start).Scan(&unknown); qerr != nil {
				return nil, qerr
			}
			message, refused := tool.Message(err)
			state := ledger.Failed
			if unknown || !refused {
				state = ledger.Unknown
			}
			if settleErr := proposals.SettleAction(ctx, e.DB.Write, held, action, nil, state, &text, e.now()); settleErr != nil {
				return nil, settleErr
			}
			if refused {
				return nil, proposals.ProposalError(message)
			}
			return nil, err
		}
		if err := proposals.SettleAction(ctx, e.DB.Write, held, action, nil, ledger.OK, nil, e.now()); err != nil {
			return nil, err
		}
		return map[string]any{"summary": t.Approval.Label + ": done", "result": value}, nil
	}
}
