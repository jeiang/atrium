package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ResearchName  application.Name = "assistant.research"
	LookupName    application.Name = "assistant.lookup"
	SearchWebName application.Name = "assistant.search-web"
	WebFetchName  application.Name = "assistant.web-fetch"
	WeatherName   application.Name = "assistant.weather"
)

var agentCallers = []application.Authority{application.Agent, application.Approved}

type ResearchInput struct {
	ThreadID    int64
	RunID       int64
	Role        string
	Task        string
	ContextIDs  []string
	AllowedURLs []string
}

type ResearchDigest struct {
	Role          string
	ChildThreadID int64
	Digest        string
	Marks         tool.Marks
}

func DescribeResearch() application.Definition {
	return application.Describe[ResearchInput, ResearchDigest](ResearchName, application.Command, agentCallers, application.External, true)
}

func ToolError(err error) error {
	var e *application.Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Code {
	case application.NotConfigured:
		return tool.NotConfigured(e.Message)
	case application.Invalid, application.Refused, application.NotFound, application.Unavailable:
		return tool.Error(e.Message)
	}
	return err
}

func childFailed(message string) error {
	return &application.Error{Code: application.Unavailable, Message: message}
}

func (s *Service) clock() func() float64 {
	if s.now == nil {
		return assistant.Now
	}
	return s.now
}

func (s *Service) childAudit(ctx context.Context, name application.Name, child int64, err error) (int64, error) {
	outcome := state.OK
	if err != nil {
		var e *application.Error
		known := false
		if errors.As(err, &e) {
			outcome, known = outcomeOf(e)
		}
		if !known {
			if child == 0 {
				return 0, err
			}
			outcome = state.Failed
		}
	}
	id, aerr := s.audit(context.WithoutCancel(ctx), nil, name, target("thread", child), outcome)
	if err != nil {
		if aerr != nil {
			return 0, errors.Join(err, aerr)
		}
		return 0, err
	}
	return id, aerr
}

func (s *Service) runChild(ctx context.Context, child, parentRun int64, text, label string, limits journal.Limits) error {
	parent := parentRun
	_, err := s.runs().RunOnce(ctx, child, text, service.Start{Kind: threads.Subagent, ParentRunID: &parent, Limits: limits})
	var exceeded inference.BudgetExceeded
	if errors.As(err, &exceeded) {
		return &application.Error{Code: application.Refused, Message: fmt.Sprintf("%s not started: %s", label, exceeded), Cause: err}
	}
	return err
}

func (s *Service) finishedRun(ctx context.Context, child int64, label string) (*journal.Run, error) {
	runs, err := journal.RunsOf(ctx, s.db.Read, child)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%s: child thread %d has no run", label, child)
	}
	run := runs[len(runs)-1]
	return &run, nil
}

func stopReason(run *journal.Run) string {
	if run.Error != nil && *run.Error != "" {
		return *run.Error
	}
	return run.State
}

func (s *Service) Research(ctx context.Context, in ResearchInput) (application.Change[ResearchDigest], error) {
	var child int64
	out, err := s.research(ctx, in, &child)
	auditID, err := s.childAudit(ctx, ResearchName, child, err)
	if err != nil {
		return application.Change[ResearchDigest]{}, err
	}
	return application.Change[ResearchDigest]{Data: out, AuditID: auditID}, nil
}

func (s *Service) research(ctx context.Context, in ResearchInput, child *int64) (ResearchDigest, error) {
	maxTask := assistant.TuningAgentSubagentTaskChars
	if utf8.RuneCountInString(in.Task) > maxTask {
		return ResearchDigest{}, invalidInput(fmt.Sprintf("task is longer than %d characters", maxTask))
	}
	text := in.Task
	if len(in.ContextIDs) > 0 {
		text += "\n\nStarting points from the parent conversation (ids): " + strings.Join(in.ContextIDs, ", ")
	}
	agent, err := subagents.Get(ctx, s.db.Read, in.Role)
	if err != nil {
		return ResearchDigest{}, err
	}
	if agent == nil || !agent.Enabled {
		enabled, err := subagents.Enabled(ctx, s.db.Read)
		if err != nil {
			return ResearchDigest{}, err
		}
		names := make([]string, len(enabled))
		for i, a := range enabled {
			names[i] = a.Name
		}
		return ResearchDigest{}, invalidInput("role must be one of: " + strings.Join(names, ", "))
	}
	now := s.clock()
	if err := subagents.Slots.Acquire(ctx, assistant.TuningAgentSubagentMaxConcurrent); err != nil {
		return ResearchDigest{}, err
	}
	defer subagents.Slots.Release()
	title := in.Role + ": " + threads.TitleFrom(in.Task)
	auth, err := threads.ReadAuthority(ctx, s.db.Read, in.ThreadID)
	if err != nil {
		return ResearchDigest{}, err
	}
	if err := s.db.WithTx(ctx, func(tx *sql.Tx) (err error) {
		*child, err = subagents.CreateChild(ctx, tx, in.ThreadID, in.RunID, *agent, title, auth.ProjectID, auth.Effective(), now())
		return err
	}); err != nil {
		return ResearchDigest{}, err
	}
	if slices.Contains(agent.Tools, "web_fetch") {
		if err := threads.AddAllowedURLs(ctx, s.db, *child, in.AllowedURLs); err != nil {
			return ResearchDigest{}, err
		}
	}
	set := assistant.Read(ctx, s.db.Read)
	steps, cost := int64(assistant.TuningAgentSubagentMaxSteps), set.Float(assistant.KeySubagentMaxCostUSD)
	if err := set.Err(); err != nil {
		return ResearchDigest{}, err
	}
	extra, err := subagents.SystemAddendum(ctx, s.db.Read, *agent)
	if err != nil {
		return ResearchDigest{}, err
	}
	if err := s.runChild(ctx, *child, in.RunID, text, in.Role+" research", journal.Limits{
		MaxSteps: &steps, MaxCostUSD: &cost, SystemExtra: &extra, AllowedTools: agent.Tools,
	}); err != nil {
		return ResearchDigest{}, err
	}
	marks, err := threads.ReadMarks(ctx, s.db.Read, *child)
	if err != nil {
		return ResearchDigest{}, err
	}
	if err := threads.AddMarks(context.WithoutCancel(ctx), s.db.Write, in.ThreadID, now(), marks.Set|tool.Untrusted); err != nil {
		return ResearchDigest{}, err
	}
	run, err := s.finishedRun(ctx, *child, "research")
	if err != nil {
		return ResearchDigest{}, err
	}
	digest, err := subagents.DigestOf(ctx, s.db.Read, *child)
	if err != nil {
		return ResearchDigest{}, err
	}
	if run.State != journal.Done {
		partial := ""
		if digest != "" {
			partial = "\nPartial digest:\n" + digest
		}
		return ResearchDigest{}, childFailed(fmt.Sprintf("%s research stopped: %s (child thread %d)%s", in.Role, stopReason(run), *child, partial))
	}
	return ResearchDigest{Role: in.Role, ChildThreadID: *child, Digest: digest, Marks: marks.Set | tool.Untrusted}, nil
}

func invalidInput(message string) error {
	return &application.Error{Code: application.Invalid, Message: message}
}
