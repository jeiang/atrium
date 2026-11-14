package application

import (
	"context"
	"net/http"
	"time"

	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
)

type (
	Listed         = threads.Listed
	Thread         = threads.Thread
	OptionError    = threads.OptionError
	Item           = service.Item
	RunActive      = service.RunActive
	NothingToRedo  = service.NothingToRedo
	UnknownThread  = service.UnknownThread
	NotInterrupted = service.NotInterrupted
	Refused        = references.Refused
	BudgetExceeded = inference.BudgetExceeded
	Run            = journal.Run
	StoredProject  = projects.Project
	FileRow        = files.Row
	FileLimits     = files.Limits
	FileError      = files.FileError
	UploadedFile   = files.UploadedFile
	StreamLive     = progress.Live
	StreamSpec     = progress.Stream
)

const (
	ChatKind        = threads.Chat
	ChatThreadLimit = config.TuningChatThreadLimit
	RunRunning      = journal.Running
)

var (
	ErrStreamExpired = progress.ErrExpired
	ErrStreamFuture  = progress.ErrFuture
)

func MessageCapError(cap int) FileError { return files.MessageCapError(cap) }

func AcquireStream(key string) (func(), error) { return progress.Acquire(key) }

func ThreadComposer(ctx context.Context, t Thread, live int64) ComposerView {
	return Composer(ctx, ComposerState{Model: t.Model, Effort: t.Effort, Thinking: t.Thinking,
		ContextLimit: threads.ContextLimitOf(t), WebSearch: bool(t.WebSearch), CostCap: t.CostCapUSD, Live: live})
}

func (s *Service) UploadLimits(ctx context.Context) (FileLimits, error) {
	return files.ReadLimits(ctx, s.db.Read)
}

func (s *Service) Location(ctx context.Context) (*time.Location, error) {
	return config.From(ctx).Location(s.env)
}

func (s *Service) RunState(ctx context.Context, runID int64) (string, bool, error) {
	run, err := journal.GetRun(ctx, s.db.Read, runID)
	if err != nil || run == nil {
		return "", false, err
	}
	return run.State, run.State == journal.Running, nil
}

func (s *Service) ServeStream(ctx context.Context, w http.ResponseWriter, stream StreamSpec, live StreamLive) error {
	return progress.Serve(ctx, w, s.db.Read, stream, live)
}
