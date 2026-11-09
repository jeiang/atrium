package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/budget"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/tool"
)

var log = logx.Logger("atrium.agent.service")

type RunActive string

func (e RunActive) Error() string { return string(e) }

type UnknownThread int64

func (e UnknownThread) Error() string { return strconv.FormatInt(int64(e), 10) }

type NothingToRedo string

func (e NothingToRedo) Error() string { return string(e) }

type NotInterrupted int64

func (e NotInterrupted) Error() string { return fmt.Sprintf("run %d is not interrupted", int64(e)) }

type Service struct {
	DB     *db.DB
	Cfg    config.Config
	Env    config.Env
	Runner *loop.Runner
	Client *anthropic.Client
	Now    func() float64

	base   context.Context
	mu     *sync.Mutex
	wg     *sync.WaitGroup
	closed bool
}

var ErrClosed = errors.New("service: closed")

func New(ctx context.Context, d *db.DB, cfg config.Config, env config.Env, runner *loop.Runner) *Service {
	return &Service{DB: d, Cfg: cfg, Env: env, Runner: runner, base: ctx, mu: &sync.Mutex{}, wg: &sync.WaitGroup{}}
}

func (s *Service) ResolveClient(ctx context.Context) (*anthropic.Client, error) {
	if s.Client != nil {
		return s.Client, nil
	}
	key, err := secrets.Credential(ctx, s.DB.Read, secrets.AnthropicAPIKey)
	if err != nil {
		return nil, err
	}
	return anthropic.New(strings.TrimSpace(key), s.Env)
}

const catalogCheckTimeout = 30 * time.Second

func (s *Service) CheckCatalog(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, catalogCheckTimeout)
	defer cancel()
	client, err := s.ResolveClient(ctx)
	if err != nil {
		return
	}
	offered, err := client.ModelIDs(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Warn("could not list models from the Anthropic Models API; the model catalog is unchecked", logx.Err, err.Error())
		}
		return
	}
	for _, id := range pricing.IDs() {
		if !slices.Contains(offered, id) {
			log.Warn("catalog model is not offered by the Anthropic Models API", logx.Model, id)
		}
	}
}

func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) now() float64 {
	if s.Now != nil {
		return s.Now()
	}
	return assistant.Now()
}

type Start struct {
	Kind        threads.Kind
	Operator    bool
	ParentRunID *int64
	Limits      journal.Limits
	Files       []json.RawMessage
}

func (o Start) kind() tool.Kind {
	switch o.Kind {
	case threads.Cron:
		return tool.Cron
	case threads.Nightly:
		return tool.Nightly
	case threads.Subagent:
		return tool.Sub
	default:
		return tool.Chat
	}
}

func emptyLimits(l journal.Limits) bool {
	return l.MaxSteps == nil && l.MaxCostUSD == nil && l.SystemExtra == nil && l.AllowedTools == nil && l.ToolChoice == nil
}

func Load(ctx context.Context, q db.Querier, env config.Env, threadID int64, now float64) (*threads.Thread, error) {
	thread, err := threads.Get(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	if thread == nil {
		return nil, UnknownThread(threadID)
	}
	active, err := journal.ActiveRun(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, RunActive(fmt.Sprintf("thread %d already has a run in progress", threadID))
	}
	root, err := threads.RootKind(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return nil, err
	}
	if _, err := budget.Check(ctx, q, now, loc, root == threads.Cron || root == threads.Nightly); err != nil {
		return nil, err
	}
	return thread, nil
}

func (s *Service) Prepare(ctx context.Context, threadID int64, text string, now float64, o Start) (int64, error) {
	thread, err := Load(ctx, s.DB.Read, s.Env, threadID, now)
	if err != nil {
		return 0, err
	}
	runID, err := journal.StartRun(ctx, s.DB, threadID, string(o.kind()), thread.Model, thread.Effort, now, o.ParentRunID, new(journal.NewOwner()))
	if errors.Is(err, journal.ErrRunActive) {
		return 0, RunActive(fmt.Sprintf("thread %d already has a run in progress", threadID))
	}
	if err != nil {
		return 0, err
	}
	fail := func(err error) (int64, error) {
		if ferr := journal.FinishRun(ctx, s.DB.Write, runID, journal.Failed, s.now(), new(err.Error())); ferr != nil {
			return 0, ferr
		}
		return 0, err
	}
	if err := threads.SetTitle(ctx, s.DB.Write, threadID, threads.TitleFrom(text)); err != nil {
		return fail(err)
	}
	if o.Operator {
		if err := threads.AddAllowedURLs(ctx, s.DB, threadID, threads.URLsIn(text)); err != nil {
			return fail(err)
		}
	}
	if o.ParentRunID == nil {
		if err := progress.ClearThread(ctx, s.DB.Write, threadID); err != nil {
			return fail(err)
		}
	}
	if !emptyLimits(o.Limits) {
		if err := journal.SetLimits(ctx, s.DB.Write, runID, o.Limits); err != nil {
			return fail(err)
		}
	}
	marks, err := references.Marks(o.Files)
	if err != nil {
		return fail(err)
	}
	if err := threads.AddMarks(ctx, s.DB.Write, threadID, now, marks); err != nil {
		return fail(err)
	}
	content := append(append([]json.RawMessage{}, o.Files...), actx.TextBlock(text))
	if _, err := journal.AppendMessage(ctx, s.DB, threadID, &runID, journal.User, content, now, nil); err != nil {
		return fail(err)
	}
	return runID, nil
}

func (s *Service) PrepareRerun(ctx context.Context, threadID int64, now float64) (int64, error) {
	thread, err := Load(ctx, s.DB.Read, s.Env, threadID, now)
	if err != nil {
		return 0, err
	}
	if err := progress.ClearThread(ctx, s.DB.Write, threadID); err != nil {
		return 0, err
	}
	runID, err := journal.StartRun(ctx, s.DB, threadID, string(tool.Chat), thread.Model, thread.Effort, now, nil, new(journal.NewOwner()))
	if errors.Is(err, journal.ErrRunActive) {
		return 0, RunActive(fmt.Sprintf("thread %d already has a run in progress", threadID))
	}
	return runID, err
}

func (s *Service) Execute(ctx context.Context, runID int64) (*journal.Run, error) {
	client, err := s.ResolveClient(ctx)
	var nc config.NotConfigured
	if errors.As(err, &nc) {
		if err := journal.FinishRun(ctx, s.DB.Write, runID, journal.Failed, s.now(), new(nc.Error())); err != nil {
			return nil, err
		}
		return journal.GetRun(ctx, s.DB.Read, runID)
	}
	if err != nil {
		return nil, err
	}
	stored, err := journal.LimitsOf(ctx, s.DB.Read, runID)
	if err != nil {
		return nil, err
	}
	return s.Runner.Run(ctx, s.DB, s.Cfg, s.Env, client, runID, loop.Options{Now: s.now, Embed: s.embed, Limits: stored})
}

func (s *Service) embed(ctx context.Context, texts []string) ([][]float64, error) {
	url, err := embed.EmbedURL(ctx, s.DB.Read)
	if err != nil {
		return nil, err
	}
	ctx, err = embed.WithModels(ctx, s.DB.Read)
	if err != nil {
		return nil, err
	}
	return embed.Texts(ctx, url, texts)
}

func (s *Service) RunOnce(ctx context.Context, threadID int64, text string, o Start) (int64, error) {
	runID, err := s.Prepare(ctx, threadID, text, s.now(), o)
	if err != nil {
		return 0, err
	}
	if _, err := s.Execute(ctx, runID); err != nil {
		return 0, err
	}
	return runID, nil
}

func (s *Service) inBackground(runID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.wg.Go(func() {
		defer func() {
			if p := recover(); p != nil {
				log.Error(fmt.Sprintf("background run %d crashed", runID), logx.RunID, runID, logx.Err, fmt.Sprintf("panic: %v\n%s", p, debug.Stack()))
			}
		}()
		if _, err := s.Execute(s.base, runID); errors.Is(err, context.Canceled) {
			log.Info(fmt.Sprintf("background run %d cancelled", runID), logx.RunID, runID)
		} else if err != nil {
			log.Error(fmt.Sprintf("background run %d crashed", runID), logx.RunID, runID, logx.Err, err)
		}
	})
	return nil
}

func (s *Service) launch(ctx context.Context, runID int64) error {
	if err := s.inBackground(runID); err != nil {
		if ferr := journal.FinishRun(ctx, s.DB.Write, runID, journal.Failed, s.now(), new(err.Error())); ferr != nil {
			return ferr
		}
		return err
	}
	return nil
}

func (s *Service) StartRun(ctx context.Context, threadID int64, text string, o Start) (int64, error) {
	ctx = context.WithoutCancel(ctx)
	runID, err := s.Prepare(ctx, threadID, text, s.now(), o)
	if err != nil {
		return 0, err
	}
	return runID, s.launch(ctx, runID)
}

type Sent struct {
	Queued bool
	ID     int64
}

func (s *Service) Send(ctx context.Context, threadID int64, text string, attachments []json.RawMessage) (Sent, error) {
	ctx = context.WithoutCancel(ctx)
	thread, err := threads.Get(ctx, s.DB.Read, threadID)
	if err != nil {
		return Sent{}, err
	}
	if thread == nil {
		return Sent{}, UnknownThread(threadID)
	}
	refs, err := references.Resolve(ctx, s.DB.Read, threadID, text)
	if err != nil {
		return Sent{}, err
	}
	attachments = append(attachments, refs...)
	id, queued, err := journal.QueueMessage(ctx, s.DB, threadID, text, s.now(), attachments)
	if err != nil {
		return Sent{}, err
	}
	if queued {
		return Sent{Queued: true, ID: id}, nil
	}
	runID, err := s.StartRun(ctx, threadID, text, Start{Kind: threads.Chat, Operator: true, Files: attachments})
	return Sent{ID: runID}, err
}

func (s *Service) lastOperator(ctx context.Context, threadID int64, refusal string) (*journal.Message, error) {
	if _, err := Load(ctx, s.DB.Read, s.Env, threadID, s.now()); err != nil {
		return nil, err
	}
	history, err := journal.LoadHistory(ctx, s.DB.Read, threadID)
	if err != nil {
		return nil, err
	}
	last, err := history.LastOperator()
	if err != nil {
		return nil, err
	}
	if last == nil {
		return nil, NothingToRedo(refusal)
	}
	return last, nil
}

func (s *Service) EditLast(ctx context.Context, threadID int64, text string, attachments []json.RawMessage) (int64, error) {
	ctx = context.WithoutCancel(ctx)
	last, err := s.lastOperator(ctx, threadID, "no operator message to edit")
	if err != nil {
		return 0, err
	}
	refs, err := references.Resolve(ctx, s.DB.Read, threadID, text)
	if err != nil {
		return 0, err
	}
	attachments = append(attachments, refs...)
	if err := s.supersede(ctx, threadID, last.Seq, false); err != nil {
		return 0, err
	}
	return s.StartRun(ctx, threadID, text, Start{Kind: threads.Chat, Operator: true, Files: attachments})
}

func (s *Service) Regenerate(ctx context.Context, threadID int64) (int64, error) {
	ctx = context.WithoutCancel(ctx)
	last, err := s.lastOperator(ctx, threadID, "no operator message to answer again")
	if err != nil {
		return 0, err
	}
	if err := s.supersede(ctx, threadID, last.Seq, true); err != nil {
		return 0, err
	}
	runID, err := s.PrepareRerun(ctx, threadID, s.now())
	if err != nil {
		return 0, err
	}
	return runID, s.launch(ctx, runID)
}

func (s *Service) supersede(ctx context.Context, threadID, fromSeq int64, keepMessage bool) error {
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := journal.Supersede(ctx, tx, threadID, fromSeq, keepMessage); err != nil {
			return err
		}
		if err := actx.Supersede(ctx, tx, threadID, fromSeq); err != nil {
			return err
		}
		return memory.Rebuild(ctx, tx, threadID, s.now())
	})
}

func (s *Service) DeleteThread(ctx context.Context, threadID int64) error {
	thread, err := threads.Get(ctx, s.DB.Read, threadID)
	if err != nil {
		return err
	}
	if thread == nil || thread.Kind != threads.Chat {
		return UnknownThread(threadID)
	}
	children, err := workspace.ChildThreads(ctx, s.DB.Read, threadID)
	if err != nil {
		return err
	}
	for _, row := range append([]threads.Thread{*thread}, children...) {
		active, err := journal.ActiveRun(ctx, s.DB.Read, row.ID)
		if err != nil {
			return err
		}
		if active != nil {
			return RunActive(fmt.Sprintf("thread %d has a run in progress", row.ID))
		}
	}
	for _, row := range append(children, *thread) {
		if _, err := files.DeleteOwned(ctx, s.DB.Write, s.Cfg.FilesDir(), files.ThreadOwner(row.ID)); err != nil {
			return err
		}
		if err := threads.Purge(ctx, s.DB.Write, row.ID, s.now()); err != nil {
			return err
		}
	}
	return nil
}

func NeedsOperator(ctx context.Context, q db.Querier, run journal.Run) (bool, error) {
	last, pending, err := loop.PendingUses(ctx, q, run.ThreadID)
	if err != nil {
		return false, err
	}
	for _, use := range pending {
		var runID int64
		if last.RunID != nil {
			runID = *last.RunID
		}
		call, err := journal.GetToolCall(ctx, q, runID, use.ID)
		if err != nil {
			return false, err
		}
		if call != nil && call.ReplayClass == journal.Write && call.State == journal.Started {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) claim(ctx context.Context, runID int64) error {
	ok, err := journal.ClaimResume(ctx, s.DB.Write, runID, journal.NewOwner(), s.now())
	if err != nil {
		return err
	}
	if !ok {
		return NotInterrupted(runID)
	}
	return nil
}

func (s *Service) StartResume(ctx context.Context, runID int64) error {
	if err := s.claim(context.WithoutCancel(ctx), runID); err != nil {
		return err
	}
	return s.inBackground(runID)
}

func (s *Service) ResumeInterrupted(ctx context.Context, background bool) (int, error) {
	if _, err := journal.MarkInterrupted(ctx, s.DB.Write, s.now()); err != nil {
		return 0, err
	}
	found, err := journal.InterruptedRuns(ctx, s.DB.Read)
	if err != nil {
		return 0, err
	}
	resumed := 0
	for _, run := range found {
		waiting, err := NeedsOperator(ctx, s.DB.Read, run)
		if err != nil {
			return 0, err
		}
		if waiting {
			continue
		}
		ok, err := journal.ClaimResume(ctx, s.DB.Write, run.ID, journal.NewOwner(), s.now())
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if background {
			if err := s.inBackground(run.ID); err != nil {
				return 0, err
			}
		} else if _, err := s.Execute(ctx, run.ID); err != nil {
			return 0, err
		}
		resumed++
	}
	return resumed, nil
}
