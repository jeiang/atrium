package assistantmodule

import (
	"context"
	"slices"
	"sync"

	"github.com/go-chi/chi/v5"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/scheduler/jobcmd"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	"github.com/jeiang/atrium/internal/assistant/anthropicconn"
	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/digest"
	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/schedulecmd"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents/lookup"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/subagents/websearch"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tools/askoperator"
	attentiontool "github.com/jeiang/atrium/internal/assistant/tools/attention"
	dependencytools "github.com/jeiang/atrium/internal/assistant/tools/dependencies"
	filetools "github.com/jeiang/atrium/internal/assistant/tools/files"
	kbtools "github.com/jeiang/atrium/internal/assistant/tools/kb"
	memorytools "github.com/jeiang/atrium/internal/assistant/tools/memory"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	taskstools "github.com/jeiang/atrium/internal/assistant/tools/tasks"
	webfetchtools "github.com/jeiang/atrium/internal/assistant/tools/webfetch"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/inference/opsmetrics"
	"github.com/jeiang/atrium/internal/kb"
	kbembedding "github.com/jeiang/atrium/internal/kb/embedding"
	"github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/review"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tasks"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Now   func() float64
	Jobs  jobworker.Dependencies
	Open  func(context.Context) (context.Context, *service.Service, func(), error)
	Start func(context.Context) error
}

func (m Module) Declare(mc module.Context) module.Declaration {
	knowledgeOps, knowledgeBindings := m.knowledgeOperations()
	taskOps, taskBindings := m.taskOperations()
	projectOps, projectBindings := spaceOperations()
	threadOps, threadBindings := threadOperations()
	fileOps, fileBindings := fileOperations()
	scheduleOps, scheduleBindings := scheduleOperations()
	memoryOps, memoryBindings := memoryOperations()
	researchOps, researchBindings := researchOperations()
	agentOps, agentBindings := agentOperations()
	attentionOps, attentionBindings := attentionOperations()
	dependencyOps, dependencyBindings := dependencyOperations()
	jobOps, jobBindings := jobOperations()
	return module.Declaration{
		Name:  unit.Assistant,
		Title: "Assistant",
		Needs: []module.Need{
			{Kind: anthropicconn.Kind, Soft: true, Capability: "models", Features: []string{"assistant chat", "scheduled runs", "web search"}},
			{Kind: webfetch.KindWebfetch, Soft: true, Capability: "egress", Features: []string{"web fetch"}},
		},
		Packages: []string{"internal/assistant", "internal/attention/application", "internal/attention/sources", "internal/commitments", "internal/kb", "internal/tasks"},
		Tables: []string{"context_ledger", "context_projections", "export_pages", "kb_fts", "kb_records", "memory_fts", "memory_jobs", "memory_nodes", "memory_state", "memory_views",
			"projects", "record_links", "run_events", "run_streams", "runs", "skills", "subagents", "tasks", "thread_files", "thread_fts", "thread_messages", "thread_plans",
			"thread_queue", "threads", "tool_calls", "tool_results"},
		EffectOrigins:    []module.EffectOrigin{{Name: "export", Direct: true}, {Name: "agent", Direct: true}, {Name: "local", Direct: true}},
		Channels:         []string{notify.Default},
		EffectCategories: []review.Category{{Name: unit.Assistant}, {Name: "knowledge", Operations: []string{"kb"}}},
		Refs:             asstapp.Refs(),
		HasOperatorData:  threads.HasRows,
		Requires:         []module.Requirement{module.ConnectionHealth(string(anthropicconn.Kind))},
		Operations:       slices.Concat(knowledgeOps, taskOps, projectOps, threadOps, fileOps, scheduleOps, memoryOps, agentOps, researchOps, attentionOps, dependencyOps, jobOps),
		Bindings:         slices.Concat(knowledgeBindings, taskBindings, projectBindings, threadBindings, fileBindings, scheduleBindings, memoryBindings, agentBindings, researchBindings, attentionBindings, dependencyBindings, jobBindings, finalBindings()),
		Settings: slices.Concat(assistant.Settings(), digest.Settings(),
			registry.Settings(), asstapp.SearchWebSettings(), inference.Settings(),
			export.Settings(), scheduler.Settings(unit.Assistant)),
		Host: export.HostSettings(),
		Recover: func(ctx context.Context, d *db.DB, _ config.Config) error {
			return kb.EnsureInstructions(ctx, d, prompts.System, assistant.Now())
		},
		Sections:  export.Sections(),
		Attention: AttentionSources(mc.Env),
		Search:    SearchSources(),
		Metrics:   opsmetrics.Metrics(),
		Queues:    []ops.Queue{{Name: "thread_queue", Count: journal.QueueDepth}},
		Corpora:   []embed.Corpus{kbembedding.Corpus},
		Jobs:      Jobs(m.Jobs),
		Services: []runtime.ServiceDefinition{{Name: "assistant", Run: func(ctx context.Context, _ *runtime.Services) error {
			if err := m.Start(ctx); err != nil {
				return err
			}
			return module.Idle(ctx)
		}}},
		Pages: []web.Page{{
			Module: func(_ chi.Router, d *web.Deps) {
				svc := sync.OnceValue(func() *asstapp.Service { return pageAssistant(m.Jobs.Service)(d) })
				threadAPI(d, svc, runEvents(d, svc))
				d.OnShell(func(ctx context.Context, s *web.ShellState) error {
					n, err := threads.RunningChats(ctx, d.DB.Read)
					s.ChatsRunning = n
					return err
				})
			},
			Nav: []web.NavItem{
				{Group: "Assistant", Label: "Chat", URL: "/chat", Icon: "messages-square", Page: "chat", Brand: true, Order: 10},
				{Section: "library", Label: "Knowledge", URL: "/knowledge", Icon: "book-open", Page: "library_knowledge", Order: 5},
				{Section: "library", Label: "Files", URL: "/knowledge/files", Icon: "paperclip", Page: "library_files", Order: 20},
				{Section: "library", Label: "Scheduled", URL: "/assistant/routines", Icon: "clock", Page: "scheduled", Order: 40},
				{Label: "Assistant", URL: "/assistant/behavior", Page: "assistant", Settings: true, Primary: true, PrimaryURL: "/settings", Order: 10},
			},
		}, {Module: func(_ chi.Router, d *web.Deps) { m.knowledgeRoutes(d); m.taskRoutes(d); attentionRoutes(d) }}},
		Groups:   SettingGroups(),
		Commands: []*cli.Command{jobcmd.DelegatingSubs(agentcmd.Command(mc.Enabled, agentOpen(m.Open), agentTick(m.Open)), map[string]string{"tick": ""}), memorycmd.Command(memoryCLIService(mc.Env, m.Now)), schedulecmd.Command(scheduleCLIService(mc.Env, m.Now))},
		Tools:    ToolDefinitions(),
		Effects: func(d module.EffectDeps) module.Effects {
			a := &approval.Executor{DB: d.DB, Env: d.Env, Now: d.Now, Ledger: d.Ledger, Tools: d.Approvals, Registry: d.Tools}
			return module.Effects{
				Proposals: slices.Concat(a.Proposals(), permissionProposals(d.DB, d.Resume)),
				Undo:      slices.Concat(asstapp.FileUndo(), kb.Undo(), tasksapp.Undo(), asstapp.ScheduleUndo(), export.Undo(d.Ledger), []ledger.UndoDefinition{attention.SnoozeUndoDefinition(attention.UndoTaskSnooze, d.Now)}),
				Resolve:   export.Resolve(d.Ledger),
			}
		},
		Connections: slices.Concat(anthropicconn.Connections(), webfetch.Connections()),
	}
}

func pageAssistant(runs func(context.Context) (*service.Service, error)) func(*web.Deps) *asstapp.Service {
	return func(d *web.Deps) *asstapp.Service {
		svc := sync.OnceValue(func() *service.Service {
			s, err := runs(d.Ctx)
			if err != nil {
				panic(err)
			}
			return s
		})
		return assistantService(d).WithRuns(svc)
	}
}

func AttentionSources(env config.Env) []attention.Source {
	return []attention.Source{
		attentionsources.Questions(), tasks.AttentionSource(),
		dreaming.Attention(env),
	}
}

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "spending", Title: "Spending", Keys: []string{inference.ThreadCapKey, inference.RunCapKey, assistant.KeySubagentMaxCostUSD, asstapp.KeySearchWebMaxCostUSD, assistant.KeyDreamingMaxCost, assistant.KeyDreamingMonthly, classifier.OpsRunCapKey, classifier.OpsMonthlyCapKey}},
		{Name: "background", Title: "Background work", Keys: []string{
			assistant.KeyEffortCron, assistant.KeyEffortNightly, assistant.KeyEffortSubagent, asstapp.KeySearchWebModel,
		}},
		{Name: "schedules", Title: "Schedules", Keys: []string{digest.NotifyKey, scheduler.KeyMinInterval}},
	}
}

func ToolDefinitions() []module.ToolDefinition {
	defs := []module.ToolDefinition{{Tool: registry.WebSearchTool()}, proposetools.Declared()}
	for _, tools := range [][]tool.Tool{
		askoperator.Tools(memoryToolService), attentiontool.Tools(attentionToolService), filetools.Tools(fileToolService), memorytools.Tools(memoryToolService),
		webfetchtools.Tools(memoryToolService), recalltools.Tools(memoryToolService), plantools.Tools(memoryToolService),
		scheduletools.Tools(scheduleToolService), taskstools.Tools(taskToolService, commitmentToolService),
		kbtools.Tools(knowledgeToolService),
		dependencytools.Tools(dependencyToolService),
	} {
		for _, t := range tools {
			defs = append(defs, module.ToolDefinition{Tool: t})
		}
	}
	return append(defs, research.Definition(), lookup.Definition(), websearch.Definition(), report.Definition())
}
