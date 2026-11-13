package application

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	AgentWriteName   application.Name = "knowledge.agent-write"
	AgentRetractName application.Name = "knowledge.agent-retract"
	agentTitleChars                   = 200
	agentReasonChars                  = 200
)

var AgentBodyBound = tool.NewCeiling(assistant.TuningAgentKbWriteChars, 200, assistant.TuningAgentKbWriteChars)

type AgentSource struct {
	ThreadID int64
	Seq      *int64
}

type AgentWriteInput struct {
	ID      int64
	Title   string
	Body    string
	Aliases []string
	Scope   string
	Source  *AgentSource
}

type AgentRetractInput struct {
	ID     int64
	Reason string
}

type Written struct {
	ID int64
}

func describeAgent[I, O any](name application.Name) application.Definition {
	return application.Describe[I, O](name, application.Command, []application.Authority{application.Agent}, application.LocalUndoable, true)
}

func DescribeAgentWrite() application.Definition {
	return describeAgent[AgentWriteInput, Written](AgentWriteName)
}

func DescribeAgentRetract() application.Definition {
	return describeAgent[AgentRetractInput, Written](AgentRetractName)
}

type agentRun struct {
	writer  kb.Writer
	nightly bool
	sources []int64
}

func refusal(message string) error {
	return &application.Error{Code: application.Refused, Message: message}
}

func (s *Service) agentRun(ctx context.Context, q db.Querier) (agentRun, error) {
	inv, ok := application.InvocationOf(ctx)
	if !ok || inv.RunID == 0 {
		return agentRun{}, refusal("knowledge can only be written from an assistant run")
	}
	run, err := journal.GetRun(ctx, q, inv.RunID)
	if err != nil {
		return agentRun{}, err
	}
	if run == nil || run.ThreadID != inv.ThreadID {
		return agentRun{}, refusal("this run is gone")
	}
	limits, err := journal.LimitsOf(ctx, q, inv.RunID)
	if err != nil {
		return agentRun{}, err
	}
	kind, _ := tool.ParseKind(run.Kind)
	return agentRun{
		writer:  kb.Writer{ThreadID: inv.ThreadID, RunID: inv.RunID, Kind: string(kind), Now: s.now()},
		nightly: kind == tool.Nightly,
		sources: limits.Sources,
	}, nil
}

func agentFailure(err error) error {
	var refused tool.Error
	if errors.As(err, &refused) {
		return &application.Error{Code: application.Refused, Message: refused.Error(), Cause: err}
	}
	return err
}

func (s *Service) agentCommand(ctx context.Context, name application.Name, id int64, check func(agentRun) error, effect func(agentRun) ledger.New, apply func(context.Context, *sql.Tx, agentRun) (kb.Record, ledger.Result, error)) (application.Change[Written], error) {
	var effectID, auditN int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		run, err := s.agentRun(ctx, tx)
		if err != nil {
			return err
		}
		if err := check(run); err != nil {
			return err
		}
		var rec kb.Record
		applied, err := s.ledger.ApplyLocalTx(ctx, tx, effect(run), func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
			next, res, err := apply(ctx, tx, run)
			rec = next
			return res, err
		})
		if err != nil {
			return err
		}
		effectID, id = applied.ID, rec.ID
		auditN, err = s.audit(ctx, tx, name, id, state.OK, effectID)
		return err
	})
	if err != nil {
		return application.Change[Written]{}, s.reject(ctx, name, id, agentFailure(err))
	}
	return application.Change[Written]{Data: Written{ID: id}, EffectIDs: []int64{effectID}, AuditID: auditN}, nil
}

func (s *Service) AgentWrite(ctx context.Context, in AgentWriteInput) (application.Change[Written], error) {
	if utf8.RuneCountInString(in.Title) > agentTitleChars {
		return application.Change[Written]{}, s.reject(ctx, AgentWriteName, in.ID, invalid("title", "title is limited to 200 characters"))
	}
	if err := AgentBodyBound.ClampText(ctx, s.db.Read, in.Body, "body"); err != nil {
		var bound tool.Error
		if errors.As(err, &bound) {
			err = invalid("body", bound.Error())
		}
		return application.Change[Written]{}, s.reject(ctx, AgentWriteName, in.ID, err)
	}
	write := kb.WriteInput{ID: in.ID, Title: in.Title, Body: in.Body, Aliases: in.Aliases, Scope: in.Scope}
	if in.Source != nil {
		write.Source = &kb.Source{ThreadID: in.Source.ThreadID, Seq: in.Source.Seq}
	}
	return s.agentCommand(ctx, AgentWriteName, in.ID, func(run agentRun) error {
		if run.nightly && (write.Source == nil || !slices.Contains(run.sources, write.Source.ThreadID)) {
			return refusal("source.thread_id must be one of the threads gathered for this run")
		}
		return nil
	}, func(run agentRun) ledger.New {
		return run.writer.Effect("kb_write", 0)
	}, func(ctx context.Context, tx *sql.Tx, run agentRun) (kb.Record, ledger.Result, error) {
		return kb.AgentWriteTx(ctx, tx, run.writer, write)
	})
}

func (s *Service) AgentRetract(ctx context.Context, in AgentRetractInput) (application.Change[Written], error) {
	if utf8.RuneCountInString(in.Reason) > agentReasonChars {
		return application.Change[Written]{}, s.reject(ctx, AgentRetractName, in.ID, invalid("reason", "reason is limited to 200 characters"))
	}
	return s.agentCommand(ctx, AgentRetractName, in.ID, func(agentRun) error { return nil }, func(run agentRun) ledger.New {
		return run.writer.Effect("kb_retract", in.ID)
	}, func(ctx context.Context, tx *sql.Tx, run agentRun) (kb.Record, ledger.Result, error) {
		if run.nightly {
			rec, err := kb.Get(ctx, tx, in.ID)
			if err != nil {
				return kb.Record{}, ledger.Result{}, err
			}
			if rec == nil || rec.SourceThreadID == nil || !slices.Contains(run.sources, *rec.SourceThreadID) {
				return kb.Record{}, ledger.Result{}, tool.Error("only records from the threads gathered for this run can be retracted")
			}
		}
		return kb.AgentRetractTx(ctx, tx, run.writer, in.ID, in.Reason)
	})
}
