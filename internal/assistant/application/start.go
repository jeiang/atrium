package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	ThreadStartName application.Name = "assistant.thread.start"
	MessageSendName application.Name = "assistant.message.send"
	MessageEditName application.Name = "assistant.message.edit"
)

type StartInput struct {
	Title        *string
	Claim        *scheduler.Claim
	Marks        threads.Marks
	Kind         threads.Kind
	ProjectID    int64
	About        string
	Model        string
	Effort       string
	ContextLimit any
	Thinking     string
	WebSearch    *bool
	CostCapUSD   *float64
	Text         string
	Files        []files.UploadedFile
	ToolChoice   string
	Operator     bool
	Wait         bool
}

type Started struct {
	ThreadID int64 `json:"thread_id"`
	RunID    int64 `json:"run_id"`
}

type ArchivedProject string

func (e ArchivedProject) Error() string { return string(e) }

type ReadOnlyThread int64

func (e ReadOnlyThread) Error() string { return "subagent threads are read-only" }

type KindMismatch struct {
	ThreadID int64
	Kind     threads.Kind
}

func (e KindMismatch) Error() string {
	return fmt.Sprintf("thread %d is a %s thread", e.ThreadID, e.Kind)
}

func DescribeThreadStart() application.Definition {
	return application.Describe[StartInput, Started](ThreadStartName, application.Command,
		[]application.Authority{application.Operator, application.System}, application.External, true)
}

func (s *Service) WithRuns(runs func() *service.Service) *Service {
	out := *s
	out.runs = runs
	return &out
}

func classify(err error) error {
	var (
		app      *application.Error
		unknown  service.UnknownThread
		option   threads.OptionError
		file     files.FileError
		mismatch KindMismatch
		active   service.RunActive
		redo     service.NothingToRedo
		archived ArchivedProject
		readOnly ReadOnlyThread
		stopped  NoActiveRun
		question NoQuestion
		answer   NotAnswered
		resume   service.NotInterrupted
		exceeded inference.BudgetExceeded
		refused  references.Refused
		disabled *module.OwnerDisabled
		missing  config.NotConfigured
	)
	code := application.Code("")
	switch {
	case err == nil, errors.As(err, &app):
		return err
	case errors.As(err, &unknown):
		code = application.NotFound
	case errors.As(err, &option), errors.As(err, &file), errors.As(err, &mismatch):
		code = application.Invalid
	case errors.As(err, &active), errors.As(err, &redo), errors.As(err, &archived), errors.As(err, &readOnly),
		errors.As(err, &stopped), errors.As(err, &question), errors.As(err, &answer), errors.As(err, &resume):
		code = application.Conflict
	case errors.As(err, &missing):
		code = application.NotConfigured
	case errors.As(err, &exceeded), errors.As(err, &refused), errors.As(err, &disabled):
		code = application.Refused
	default:
		return err
	}
	return &application.Error{Code: code, Message: err.Error(), Cause: err}
}

func (s *Service) admit(ctx context.Context, run bool) (context.Context, error) {
	ctx, err := module.BindOwner(ctx, module.Assistant)
	if err != nil || !run {
		return ctx, err
	}
	return ctx, s.connected(ctx)
}

func (s *Service) connected(ctx context.Context) error {
	_, err := s.runs().ResolveClient(ctx)
	return err
}

func (s *Service) writable(ctx context.Context, threadID int64) (*threads.Thread, error) {
	thread, err := threads.Get(ctx, s.db.Read, threadID)
	if err != nil {
		return nil, err
	}
	if thread == nil {
		return nil, service.UnknownThread(threadID)
	}
	return thread, nil
}

func (s *Service) attach(ctx context.Context, thread *threads.Thread, uploads []files.UploadedFile) ([]json.RawMessage, error) {
	if len(uploads) == 0 {
		return nil, nil
	}
	limit := threads.ContextLimitOf(*thread)
	blocks, err := files.IngestUploads(ctx, s.db, s.runs().Cfg.FilesDir(), uploads, s.now(), files.ThreadOwner(thread.ID), &limit)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(blocks))
	for i, b := range blocks {
		if out[i], err = json.Marshal(b); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) begin(ctx context.Context, threadID int64, text string, start service.Start, toolChoice string, wait bool) (int64, error) {
	if toolChoice != "" {
		start.Limits.ToolChoice = &toolChoice
	}
	if wait {
		return s.runs().RunOnce(ctx, threadID, text, start)
	}
	return s.runs().StartRun(ctx, threadID, text, start)
}

func (s *Service) finish(ctx context.Context, name application.Name, threadID, runID int64, err error) (int64, error) {
	outcome := state.OK
	if err != nil {
		outcome = state.Failed
	}
	auditID, aerr := s.audit(ctx, nil, name, target("thread", threadID), outcome)
	if aerr != nil {
		return 0, errors.Join(err, aerr)
	}
	return auditID, err
}

func (s *Service) ThreadStart(ctx context.Context, in StartInput) (application.Change[Started], error) {
	var started Started
	runID, err := s.threadStart(ctx, in, &started)
	if runID == 0 && err != nil {
		return application.Change[Started]{}, s.reject(ctx, ThreadStartName, target("thread", started.ThreadID), classify(err))
	}
	started.RunID = runID
	auditID, err := s.finish(ctx, ThreadStartName, started.ThreadID, runID, err)
	return application.Change[Started]{Data: started, AuditID: auditID}, err
}

func (s *Service) threadStart(ctx context.Context, in StartInput, started *Started) (int64, error) {
	text := strings.TrimSpace(in.Text)
	ctx, err := s.admit(ctx, text != "")
	if err != nil {
		return 0, err
	}
	kind := in.Kind
	if kind == "" {
		kind = threads.Chat
	}
	operatorText, projectID := text, in.ProjectID
	if in.About != "" && text != "" {
		about, err := s.About(ctx, AboutInput{Ref: in.About})
		if err != nil {
			return 0, err
		}
		if about := about.Data; about != nil {
			text = about.Line + "\n\n" + text
			if about.Project != 0 {
				projectID = about.Project
			}
		}
	}
	var project *int64
	if projectID != 0 {
		found, err := projects.Get(ctx, s.db.Read, projectID)
		if err != nil {
			return 0, err
		}
		if found == nil || found.ID == 0 {
			return 0, notFound(unknownProject)
		}
		if err := archivedProject(found, "start chats in it"); err != nil {
			return 0, err
		}
		project = &projectID
	}
	if text != "" {
		if err := service.Admit(ctx, s.db.Read, s.env, kind, s.now()); err != nil {
			return 0, err
		}
	}
	var threadID int64
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var jobID *int64
		if in.Claim != nil {
			jobID = &in.Claim.Job.ID
		}
		id, err := threads.Create(ctx, tx, s.now(), threads.Spec{
			Kind: kind, ProjectID: project, Model: in.Model, Effort: in.Effort, ContextLimit: in.ContextLimit,
			Thinking: in.Thinking, WebSearch: in.WebSearch, CostCapUSD: in.CostCapUSD,
			Title: in.Title, JobID: jobID, Marks: in.Marks,
		})
		if err != nil {
			return err
		}
		threadID = id
		if in.Claim != nil {
			return scheduler.AttachThread(ctx, tx, *in.Claim, id)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	started.ThreadID = threadID
	runID, err := s.firstRun(ctx, threadID, kind, text, operatorText, in)
	if runID == 0 && err != nil && in.Claim == nil {
		discarded, derr := s.runs().DiscardUnused(context.WithoutCancel(ctx), threadID)
		if discarded {
			started.ThreadID = 0
		}
		return 0, errors.Join(err, derr)
	}
	return runID, err
}

func (s *Service) firstRun(ctx context.Context, threadID int64, kind threads.Kind, text, operatorText string, in StartInput) (int64, error) {
	thread, err := threads.Get(ctx, s.db.Read, threadID)
	if err != nil {
		return 0, err
	}
	attached, err := s.attach(ctx, thread, in.Files)
	if err != nil || text == "" {
		return 0, err
	}
	refs, err := references.Resolve(ctx, s.db.Read, threadID, operatorText)
	if err != nil {
		return 0, err
	}
	return s.begin(ctx, threadID, text, service.Start{Kind: kind, Operator: in.Operator, Files: append(attached, refs...)}, in.ToolChoice, in.Wait)
}
