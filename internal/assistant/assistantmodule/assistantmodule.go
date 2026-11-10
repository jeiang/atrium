package assistantmodule

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	"github.com/jeiang/atrium/internal/assistant/nightly"
	chatpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/tools/askoperator"
	attentiontool "github.com/jeiang/atrium/internal/assistant/tools/attention"
	filetools "github.com/jeiang/atrium/internal/assistant/tools/files"
	kbtools "github.com/jeiang/atrium/internal/assistant/tools/kb"
	memorytools "github.com/jeiang/atrium/internal/assistant/tools/memory"
	opstools "github.com/jeiang/atrium/internal/assistant/tools/ops"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
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
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

func init() {
	integrations.Register(integrations.Integration{
		Name: "anthropic", Label: "Anthropic", Module: module.Assistant, SecretKey: secrets.AnthropicAPIKey,
		Help: "Console API key from a workspace with a monthly spend limit; use one workspace per instance.",
		Test: testAnthropic,
	})
}

func testAnthropic(ctx context.Context, c integrations.Conn) (string, error) {
	env := c.Env
	if env == nil {
		env = config.OS
	}
	client, err := anthropic.New(c.Secret, env, anthropic.HTTPClient(c.HTTP))
	if err != nil {
		return "", err
	}
	ids, err := client.ModelIDs(ctx)
	var failure *anthropic.APIError
	switch {
	case errors.As(err, &failure) && failure.StatusCode == http.StatusUnauthorized:
		return "", fmt.Errorf("%w: Anthropic refused the key", integrations.ErrAuth)
	case errors.As(err, &failure) && failure.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("the key may not list models: %s", failure.Message)
	case err != nil:
		return "", err
	}
	return fmt.Sprintf("%d models available", len(ids)), nil
}

type Module struct {
	Callables []map[string]worker.Callable
}

func (m Module) Register(mc module.Context) module.Registration {
	return module.Registration{
		Name:      module.Assistant,
		Attention: []attention.Source{attentionsources.Plans(), attentionsources.Questions(), tasks.AttentionSource(), serverops.AttentionSource()},
		Requires:  []module.Requirement{{Name: secrets.AnthropicAPIKey, Check: anthropicKey}},
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
				{Label: "Permissions", URL: "/settings/permissions", Page: "permissions", Settings: true, Order: 20},
				{Label: "Usage", URL: "/settings/usage", Page: "usage", Settings: true, Order: 60},
			},
		}},
		Commands: []*cli.Command{agentcmd.Command(mc.Env, mc.Tools, mc.Enabled, m.Callables...), memorycmd.Command(mc.Env)},
		Tools:    addTools,
	}
}

func anthropicKey(ctx context.Context, p module.Probe) (string, error) {
	key, err := secrets.Credential(ctx, p.Q, secrets.AnthropicAPIKey)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(key) == "" {
		return secrets.AnthropicAPIKey + " is not set", nil
	}
	return "", nil
}

func addTools(r *registry.Registry) error {
	for _, tools := range [][]tool.Tool{
		askoperator.Tools(), attentiontool.Tools(), filetools.Tools(), memorytools.Tools(),
		nightly.Tools(), webfetch.Tools(), recalltools.Tools(), plantools.Tools(),
		profiletools.Tools(), proposetools.Tools(), scheduletools.Tools(), taskstools.Tools(),
		kbtools.Tools(), weathertools.Tools(), opstools.Tools(),
	} {
		if err := r.Add(tools...); err != nil {
			return err
		}
	}
	return nil
}
