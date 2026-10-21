package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	"github.com/jeiang/atrium/internal/assistant/nightly"
	chatpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	contexttools "github.com/jeiang/atrium/internal/assistant/tools/context"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	skillstools "github.com/jeiang/atrium/internal/assistant/tools/skills"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Chat chatpages.Options
}

func (m Module) Register(mc module.Context) module.Registration {
	chat := m.Chat
	chat.Tools = mc.Tools
	return module.Registration{
		Name:     module.Assistant,
		Requires: []module.Requirement{{Name: "ANTHROPIC_API_KEY", Check: anthropicKey}},
		Pages:    []web.Page{{Module: chatpages.Module(chat), Templates: chatpages.Templates()}},
		Commands: []*cli.Command{agentcmd.Command(mc.Tools, mc.Enabled)},
		Tools:    addTools,
	}
}

func anthropicKey(_ context.Context, p module.Probe) (string, error) {
	if _, err := config.AnthropicKey(p.Env); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

func addTools(r *registry.Registry) error {
	for _, tools := range [][]tool.Tool{
		nightly.Tools(), webfetch.Tools(), contexttools.Tools(), recalltools.Tools(), plantools.Tools(),
		profiletools.Tools(), proposetools.Tools(), scheduletools.Tools(), skillstools.Tools(),
	} {
		if err := r.Add(tools...); err != nil {
			return err
		}
	}
	return nil
}
