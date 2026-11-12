package assistantmodule

import (
	"context"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	"github.com/jeiang/atrium/internal/assistant/anthropicconn"
	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/digest"
	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	chatpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
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
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/inference/opsmetrics"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/weather"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	ProposeKinds []proposals.Definition
	Now          func() float64
	Callables    []map[string]worker.Callable
}

func (m Module) Declare(mc module.Context) module.Declaration {
	return module.Declaration{
		Name: module.Assistant,
		Settings: slices.Concat(assistant.Settings(), loop.Settings(), digest.Settings(), memory.Settings(),
			prompts.Settings(), registry.Settings(), scheduler.Settings(), websearch.Settings(), inference.Settings(),
			export.Settings(), weather.SettingsDefs()),
		Host:      export.HostSettings(),
		Sections:  export.Sections(),
		Attention: AttentionSources(mc.Env),
		Metrics:   slices.Concat(scheduler.Metrics(), opsmetrics.Metrics()),
		Requires:  Requirements(),
		Pages: []web.Page{{
			Module:    chatpages.Module(chatpages.Options{Tools: mc.Tools}),
			Templates: chatpages.Templates(),
			Home:      "/chat",
			Nav: []web.NavItem{
				{Group: "Assistant", Label: "Chat", URL: "/chat", Icon: "messages-square", Page: "chat", Brand: true, Order: 10},
				{Section: "library", Label: "Knowledge", URL: "/library/knowledge", Icon: "book-open", Page: "library_knowledge", Order: 5},
				{Section: "library", Label: "Files", URL: "/library/files", Icon: "paperclip", Page: "library_files", Order: 20},
				{Section: "library", Label: "Scheduled", URL: "/scheduled", Icon: "clock", Page: "scheduled", Order: 40},
				{Label: "Assistant", URL: "/settings/assistant", Page: "assistant", Settings: true, Primary: true, PrimaryURL: "/settings", Order: 10},
			},
		}},
		Groups:   SettingGroups(),
		Commands: []*cli.Command{agentcmd.Command(mc.Env, mc.Tools, mc.Enabled, m.Now, m.Callables...), memorycmd.Command(mc.Env, m.Now)},
		Tools:    ToolDefinitions(m.ProposeKinds),
		Effects: func(d module.EffectDeps) module.Effects {
			a := &approval.Executor{DB: d.DB, Env: d.Env, Now: d.Now, MailOpen: d.Open, Ledger: d.Ledger, Tools: d.Approvals, Registry: d.Tools}
			return module.Effects{
				Proposals: slices.Concat(a.Proposals(), permissionProposals(d.Resume)),
				Undo:      slices.Concat(filetools.Undo(), kb.Undo(), taskstools.Undo(), scheduletools.Undo(), serverops.Undo(), export.Undo(d.Ledger)),
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

func Requirements() []module.Requirement {
	return []module.Requirement{{Name: "the Anthropic connection", Check: anthropicKey}}
}

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "spending", Title: "Spending", Keys: append([]string{inference.ThreadCapKey}, append(runCaps(), assistant.KeySubagentMaxCostUSD,
			websearch.KeyMaxCostUSD, assistant.KeyDreamingMaxCost, assistant.KeyDreamingMonthly, serverops.KeyWorkerMaxRunUSD, serverops.KeyWorkerMonthlyUSD)...)},
		{Name: "background", Title: "Background work", Keys: []string{
			assistant.KeyEffortCron, assistant.KeyEffortNightly, assistant.KeyEffortSubagent, websearch.KeyModel,
			memory.ModelKey, memory.EffortKey, serverops.KeyCondenser,
		}},
	}
}

func anthropicKey(ctx context.Context, p module.Probe) (string, error) {
	lease, err := connections.OpenRead(ctx, p.Q, config.From(ctx).Host(), connections.ID(anthropicconn.Kind))
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return "the Anthropic connection is not configured", nil
		}
		return "", err
	}
	defer lease.Close()
	if _, err := lease.Credential("api_key"); err != nil {
		return "the Anthropic connection has no API key", nil
	}
	return "", nil
}

func ToolDefinitions(kinds []proposals.Definition) []module.ToolDefinition {
	defs := []module.ToolDefinition{{Tool: registry.WebSearchTool()}, proposetools.Definition(kinds)}
	for _, tools := range [][]tool.Tool{
		askoperator.Tools(), attentiontool.Tools(), filetools.Tools(), memorytools.Tools(),
		webfetch.Tools(), recalltools.Tools(), plantools.Tools(),
		scheduletools.Tools(), taskstools.Tools(),
		kbtools.Tools(), weathertools.Tools(), opstools.Tools(),
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
