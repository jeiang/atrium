package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	"github.com/jeiang/atrium/internal/assistant/nightly"
	chatpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/skills/skillscmd"
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
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/secrets"
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
		Requires: []module.Requirement{{Name: secrets.AnthropicAPIKey, Check: anthropicKey}},
		Pages:    []web.Page{{Module: chatpages.Module(chat), Templates: chatpages.Templates()}},
		Commands: []*cli.Command{agentcmd.Command(mc.Tools, mc.Enabled), skillscmd.Command()},
		Tools:    addTools,
	}
}

func anthropicKey(ctx context.Context, p module.Probe) (string, error) {
	key, err := secrets.Credential(ctx, p.Q, secrets.AnthropicAPIKey)
	if err != nil {
		return "", err
	}
	if py.Strip(key) == "" {
		return secrets.AnthropicAPIKey + " is not set", nil
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
