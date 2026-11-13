package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
)

const (
	ReplyRegenerateName application.Name = "assistant.reply.regenerate"
	ThreadBranchName    application.Name = "assistant.thread.branch"
	ThreadDeleteName    application.Name = "assistant.thread.delete"
	ThreadPinName       application.Name = "assistant.thread.pin"
	ThreadArchiveName   application.Name = "assistant.thread.archive"
	ThreadProjectName   application.Name = "assistant.thread.project"
	ThreadOptionsName   application.Name = "assistant.thread.options"
	unknownChatThread                    = "unknown chat thread"
)

type FlagInput struct {
	ThreadID int64
	Set      bool
}

type MoveInput struct {
	ThreadID  int64
	ProjectID int64
}

type OptionsInput struct {
	ThreadID     int64
	Model        string
	Effort       string
	ContextLimit string
}

type BranchInput struct {
	ThreadID int64
	Seq      int64
}

type ThreadChanged struct {
	ThreadID int64 `json:"thread_id"`
	RunID    int64 `json:"run_id,omitempty"`
}

func describeThreadCommand[I any](name application.Name, effect application.Effect) application.Definition {
	return application.Describe[I, ThreadChanged](name, application.Command, []application.Authority{application.Operator}, effect, true)
}

func DescribeReplyRegenerate() application.Definition {
	return describeThreadCommand[ThreadInput](ReplyRegenerateName, application.External)
}

func DescribeThreadBranch() application.Definition {
	return describeThreadCommand[BranchInput](ThreadBranchName, application.NoEffect)
}

func DescribeThreadDelete() application.Definition {
	return describeThreadCommand[ThreadInput](ThreadDeleteName, application.NoEffect)
}

func DescribeThreadPin() application.Definition {
	return describeThreadCommand[FlagInput](ThreadPinName, application.NoEffect)
}

func DescribeThreadArchive() application.Definition {
	return describeThreadCommand[FlagInput](ThreadArchiveName, application.NoEffect)
}

func DescribeThreadProject() application.Definition {
	return describeThreadCommand[MoveInput](ThreadProjectName, application.NoEffect)
}

func DescribeThreadOptions() application.Definition {
	return describeThreadCommand[OptionsInput](ThreadOptionsName, application.NoEffect)
}

func (s *Service) inTx(ctx context.Context, name application.Name, threadID int64, apply func(tx *sql.Tx) error) (application.Change[ThreadChanged], error) {
	var auditID int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := apply(tx); err != nil {
			return err
		}
		var err error
		auditID, err = s.audit(ctx, tx, name, target("thread", threadID), state.OK)
		return err
	})
	if err != nil {
		return application.Change[ThreadChanged]{}, s.reject(ctx, name, target("thread", threadID), classify(err))
	}
	return application.Change[ThreadChanged]{Data: ThreadChanged{ThreadID: threadID}, AuditID: auditID}, nil
}

func (s *Service) act(ctx context.Context, name application.Name, threadID int64, apply func() (ThreadChanged, error)) (application.Change[ThreadChanged], error) {
	changed, err := apply()
	if err != nil && (changed.RunID != 0 || (changed.ThreadID != 0 && changed.ThreadID != threadID)) {
		_, aerr := s.audit(ctx, nil, name, target("thread", changed.ThreadID), state.Failed)
		return application.Change[ThreadChanged]{Data: changed}, errors.Join(classify(err), aerr)
	}
	if err != nil {
		return application.Change[ThreadChanged]{}, s.reject(ctx, name, target("thread", threadID), classify(err))
	}
	auditID, err := s.audit(ctx, nil, name, target("thread", changed.ThreadID), state.OK)
	return application.Change[ThreadChanged]{Data: changed, AuditID: auditID}, err
}

func (s *Service) setFlag(ctx context.Context, name application.Name, in FlagInput, set func(context.Context, db.Querier, int64, bool) (bool, error)) (application.Change[ThreadChanged], error) {
	return s.inTx(ctx, name, in.ThreadID, func(tx *sql.Tx) error {
		ok, err := set(ctx, tx, in.ThreadID, in.Set)
		if err == nil && !ok {
			err = notFound("unknown thread")
		}
		return err
	})
}

func (s *Service) ThreadPin(ctx context.Context, in FlagInput) (application.Change[ThreadChanged], error) {
	return s.setFlag(ctx, ThreadPinName, in, threads.SetPinned)
}

func (s *Service) ThreadArchive(ctx context.Context, in FlagInput) (application.Change[ThreadChanged], error) {
	return s.setFlag(ctx, ThreadArchiveName, in, threads.SetArchived)
}

func archivedProject(p *projects.Project, verb string) error {
	if p == nil || !p.Archived {
		return nil
	}
	return ArchivedProject(fmt.Sprintf("“%s” is archived. Restore it to %s.", p.Name, verb))
}

func (s *Service) ThreadProject(ctx context.Context, in MoveInput) (application.Change[ThreadChanged], error) {
	return s.inTx(ctx, ThreadProjectName, in.ThreadID, func(tx *sql.Tx) error {
		var target *int64
		if in.ProjectID != 0 {
			found, err := projects.Get(ctx, tx, in.ProjectID)
			if err != nil {
				return err
			}
			if found == nil {
				return notFound(unknownProject)
			}
			if err := archivedProject(found, "move chats into it"); err != nil {
				return err
			}
			target = &in.ProjectID
		}
		moved, err := projects.MoveThread(ctx, tx, in.ThreadID, target)
		var refused projects.ProjectError
		if errors.As(err, &refused) {
			return notFound(refused.Error())
		}
		if err == nil && !moved {
			err = notFound(unknownChatThread)
		}
		return err
	})
}

func (s *Service) ThreadOptions(ctx context.Context, in OptionsInput) (application.Change[ThreadChanged], error) {
	return s.inTx(ctx, ThreadOptionsName, in.ThreadID, func(tx *sql.Tx) error {
		changed, err := threads.SetOptions(ctx, tx, in.ThreadID, in.Model, in.Effort, in.ContextLimit)
		if err == nil && !changed {
			err = notFound(unknownChatThread)
		}
		return err
	})
}

func (s *Service) ThreadDelete(ctx context.Context, in ThreadInput) (application.Change[ThreadChanged], error) {
	return s.act(ctx, ThreadDeleteName, in.ID, func() (ThreadChanged, error) {
		return ThreadChanged{ThreadID: in.ID}, s.runs().DeleteThread(ctx, in.ID)
	})
}

func (s *Service) ReplyRegenerate(ctx context.Context, in ThreadInput) (application.Change[ThreadChanged], error) {
	return s.act(ctx, ReplyRegenerateName, in.ID, func() (ThreadChanged, error) {
		ctx, err := s.admit(ctx, false)
		if err != nil {
			return ThreadChanged{}, err
		}
		thread, err := s.writable(ctx, in.ID)
		if err != nil {
			return ThreadChanged{}, err
		}
		if thread.Kind == threads.Subagent {
			return ThreadChanged{}, ReadOnlyThread(thread.ID)
		}
		if err := s.connected(ctx); err != nil {
			return ThreadChanged{}, err
		}
		runID, err := s.runs().Regenerate(ctx, in.ID)
		return ThreadChanged{ThreadID: in.ID, RunID: runID}, err
	})
}

func (s *Service) ThreadBranch(ctx context.Context, in BranchInput) (application.Change[ThreadChanged], error) {
	return s.act(ctx, ThreadBranchName, in.ThreadID, func() (ThreadChanged, error) {
		parent, err := threads.Get(ctx, s.db.Read, in.ThreadID)
		if err != nil {
			return ThreadChanged{}, err
		}
		if parent != nil && parent.ProjectID != nil && *parent.ProjectID != 0 {
			project, err := projects.Get(ctx, s.db.Read, *parent.ProjectID)
			if err != nil {
				return ThreadChanged{}, err
			}
			if err := archivedProject(project, "start chats in it"); err != nil {
				return ThreadChanged{}, err
			}
		}
		child, err := s.runs().Branch(ctx, in.ThreadID, in.Seq)
		return ThreadChanged{ThreadID: child}, err
	})
}
