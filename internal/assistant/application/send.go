package application

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
)

type SendInput struct {
	ThreadID   int64
	Kind       threads.Kind
	Text       string
	Files      []files.UploadedFile
	ToolChoice string
	Operator   bool
	Wait       bool
}

type Sent struct {
	ThreadID int64 `json:"thread_id"`
	RunID    int64 `json:"run_id"`
	QueuedID int64 `json:"queued_id"`
}

func DescribeMessageSend() application.Definition {
	return application.Describe[SendInput, Sent](MessageSendName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func DescribeMessageEdit() application.Definition {
	return application.Describe[SendInput, Sent](MessageEditName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func (s *Service) MessageSend(ctx context.Context, in SendInput) (application.Change[Sent], error) {
	return s.message(ctx, MessageSendName, in, func(ctx context.Context, thread *threads.Thread, text string, attached []json.RawMessage) (Sent, error) {
		refs, err := references.Resolve(ctx, s.db.Read, thread.ID, text)
		if err != nil {
			return Sent{}, err
		}
		attached = append(attached, refs...)
		if !in.Wait {
			id, queued, err := journal.QueueMessage(ctx, s.db, thread.ID, text, s.now(), attached)
			if err != nil || queued {
				return Sent{QueuedID: id}, err
			}
		}
		kind := thread.Kind
		if in.Operator {
			kind = threads.Chat
		}
		runID, err := s.begin(ctx, thread.ID, text, service.Start{Kind: kind, Operator: in.Operator, Files: attached}, in.ToolChoice, in.Wait)
		return Sent{RunID: runID}, err
	})
}

func (s *Service) MessageEdit(ctx context.Context, in SendInput) (application.Change[Sent], error) {
	return s.message(ctx, MessageEditName, in, func(ctx context.Context, thread *threads.Thread, text string, attached []json.RawMessage) (Sent, error) {
		runID, err := s.runs().EditLast(ctx, thread.ID, text, attached)
		return Sent{RunID: runID}, err
	})
}

func (s *Service) message(ctx context.Context, name application.Name, in SendInput, send func(context.Context, *threads.Thread, string, []json.RawMessage) (Sent, error)) (application.Change[Sent], error) {
	sent, err := s.prepareMessage(ctx, in, send)
	if sent.RunID == 0 && sent.QueuedID == 0 && err != nil {
		return application.Change[Sent]{}, s.reject(ctx, name, target("thread", in.ThreadID), classify(err))
	}
	sent.ThreadID = in.ThreadID
	auditID, err := s.finish(ctx, name, in.ThreadID, sent.RunID, err)
	return application.Change[Sent]{Data: sent, AuditID: auditID}, err
}

func (s *Service) prepareMessage(ctx context.Context, in SendInput, send func(context.Context, *threads.Thread, string, []json.RawMessage) (Sent, error)) (Sent, error) {
	ctx, err := s.admit(ctx, true)
	if err != nil {
		return Sent{}, err
	}
	thread, err := threads.Get(ctx, s.db.Read, in.ThreadID)
	if err != nil {
		return Sent{}, err
	}
	if thread == nil {
		return Sent{}, service.UnknownThread(in.ThreadID)
	}
	if in.Kind != "" && in.Kind != thread.Kind {
		return Sent{}, KindMismatch{ThreadID: thread.ID, Kind: thread.Kind}
	}
	if in.Operator && thread.Kind == threads.Subagent {
		return Sent{}, ReadOnlyThread(thread.ID)
	}
	attached, err := s.attach(ctx, thread, in.Files)
	if err != nil {
		return Sent{}, err
	}
	return send(ctx, thread, strings.TrimSpace(in.Text), attached)
}
