package assistantmodule

import (
	"context"
	"slices"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	"github.com/jeiang/atrium/internal/assistant/anthropicconn"
	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/digest"
	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	chatpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/subagents/lookup"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/subagents/websearch"
	"github.com/jeiang/atrium/internal/assistant/tools/askoperator"
	attentiontool "github.com/jeiang/atrium/internal/assistant/tools/attention"
	filetools "github.com/jeiang/atrium/internal/assistant/tools/files"
	kbtools "github.com/jeiang/atrium/internal/assistant/tools/kb"
	memorytools "github.com/jeiang/atrium/internal/assistant/tools/memory"
	opstools "github.com/jeiang/atrium/internal/assistant/tools/ops"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	taskstools "github.com/jeiang/atrium/internal/assistant/tools/tasks"
	weathertools "github.com/jeiang/atrium/internal/assistant/tools/weather"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/inference/opsmetrics"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/kb/kbcmd"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tasks"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/weather"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	ProposeKinds []proposals.Definition
	Now          func() float64
	Jobs         jobworker.Dependencies
	Open         agentcmd.Open
	Start        func(context.Context) error
}

func (m Module) Declare(mc module.Context) module.Declaration {
	knowledgeOps, knowledgeBindings := knowledgeOperations()
	taskOps, taskBindings := taskOperations()
	projectOps, projectBindings := projectOperations()
	threadOps, threadBindings := threadOperations()
	fileOps, fileBindings := fileOperations()
	return module.Declaration{
		Name:       module.Assistant,
		Operations: slices.Concat(knowledgeOps, taskOps, projectOps, threadOps, fileOps),
		Bindings:   slices.Concat(knowledgeBindings, taskBindings, projectBindings, threadBindings, fileBindings),
		Settings: slices.Concat(assistant.Settings(), loop.Settings(), digest.Settings(), memory.Settings(),
			prompts.Settings(), registry.Settings(), websearch.Settings(), inference.Settings(),
			export.Settings(), weather.SettingsDefs()),
		Host:      export.HostSettings(),
		Sections:  export.Sections(),
		Attention: AttentionSources(mc.Env),
		Metrics:   opsmetrics.Metrics(),
		Jobs:      Jobs(m.Jobs),
		Services: []runtime.ServiceDefinition{{Name: "assistant", Run: func(ctx context.Context, _ *runtime.Services) error {
			if err := m.Start(ctx); err != nil {
				return err
			}
			return module.Idle(ctx)
		}}},
		Pages: []web.Page{{
			Module:    chatpages.Module(chatpages.Options{Service: m.Jobs.Service, Knowledge: m.knowledgeService, Assistant: assistantService}),
			Templates: chatpages.Templates(),
			Home:      "/chat",
			Nav: []web.NavItem{
				{Group: "Assistant", Label: "Chat", URL: "/chat", Icon: "messages-square", Page: "chat", Brand: true, Order: 10},
				{Section: "library", Label: "Knowledge", URL: "/library/knowledge", Icon: "book-open", Page: "library_knowledge", Order: 5},
				{Section: "library", Label: "Files", URL: "/library/files", Icon: "paperclip", Page: "library_files", Order: 20},
				{Section: "library", Label: "Scheduled", URL: "/scheduled", Icon: "clock", Page: "scheduled", Order: 40},
				{Label: "Assistant", URL: "/settings/assistant", Page: "assistant", Settings: true, Primary: true, PrimaryURL: "/settings", Order: 10},
				{Group: "Instance", Label: "Usage", URL: "/settings/usage", Page: "usage", Settings: true, Order: 60},
			},
		}},
		Groups:   SettingGroups(),
		Commands: []*cli.Command{agentcmd.Command(mc.Env, mc.Tools, mc.Enabled, m.Now, m.Open), memorycmd.Command(mc.Env, m.Now), kbcmd.Command(knowledgeCLIService(mc.Env))},
		Leaves:   map[string]module.Leaf{"kb import": {Always: true}},
		Tools:    ToolDefinitions(m.ProposeKinds),
		Effects: func(d module.EffectDeps) module.Effects {
			a := &approval.Executor{DB: d.DB, Env: d.Env, Now: d.Now, MailOpen: d.Open, Ledger: d.Ledger, Tools: d.Approvals, Registry: d.Tools}
			return module.Effects{
				Proposals: slices.Concat(a.Proposals(), permissionProposals(d.Resume)),
				Undo:      slices.Concat(asstapp.FileUndo(), kb.Undo(), tasksapp.Undo(), scheduletools.Undo(), serverops.Undo(), export.Undo(d.Ledger)),
				Resolve:   export.Resolve(d.Ledger),
			}
		},
		Connections: slices.Concat(anthropicconn.Connections(), webfetch.Connections(), weather.Connections()),
	}
}

func AttentionSources(env config.Env) []attention.Source {
	return []attention.Source{
		attentionsources.Plans(), attentionsources.Questions(), tasks.AttentionSource(),
		serverops.AttentionSource(env), dreaming.Attention(env),
	}
}

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "spending", Title: "Spending", Keys: append([]string{
			inference.BudgetKey, inference.ChatKey, inference.UnattendedKey, inference.ThreadCapKey,
		}, append(runCaps(), assistant.KeySubagentMaxCostUSD, websearch.KeyMaxCostUSD, assistant.KeyDreamingMaxCost,
			assistant.KeyDreamingMonthly, serverops.KeyWorkerMaxRunUSD, serverops.KeyWorkerMonthlyUSD)...)},
		{Name: "background", Title: "Background work", Keys: []string{
			assistant.KeyEffortCron, assistant.KeyEffortNightly, assistant.KeyEffortSubagent, websearch.KeyModel,
			memory.ModelKey, memory.EffortKey, serverops.KeyCondenser,
		}},
	}
}

func ToolDefinitions(kinds []proposals.Definition) []module.ToolDefinition {
	defs := []module.ToolDefinition{{Tool: registry.WebSearchTool()}, proposetools.Definition(kinds)}
	for _, tools := range [][]tool.Tool{
		askoperator.Tools(), attentiontool.Tools(), filetools.Tools(fileToolService), memorytools.Tools(),
		webfetch.Tools(), recalltools.Tools(), plantools.Tools(),
		scheduletools.Tools(), taskstools.Tools(taskToolService),
		kbtools.Tools(knowledgeToolService), weathertools.Tools(), opstools.Tools(),
	} {
		for _, t := range tools {
			defs = append(defs, module.ToolDefinition{Tool: t})
		}
	}
	return append(defs, research.Definition(), lookup.Definition(), websearch.Definition(), report.Definition())
}

func runCaps() []string {
	models := pricing.Models()
	keys := make([]string, len(models))
	for i, m := range models {
		keys[i] = inference.RunCapKey(m.ID)
	}
	return keys
}
