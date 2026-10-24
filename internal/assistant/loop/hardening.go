package loop

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const DoomLoopThresholdKey = "doom_loop_threshold"

var maxStepsKeys = map[tool.Kind]string{
	tool.Chat:    "max_steps_chat",
	tool.Cron:    "max_steps_cron",
	tool.Nightly: "max_steps_nightly",
}

func init() {
	instance := []settings.Scope{settings.Instance}
	defs := []settings.Def{{
		Key: DoomLoopThresholdKey, Module: "assistant", Type: settings.Int, Default: "3",
		Check: settings.Between(2, 20), Class: settings.Advanced, Scopes: instance,
	}}
	for _, kind := range []tool.Kind{tool.Chat, tool.Cron, tool.Nightly} {
		defs = append(defs, settings.Def{
			Key: maxStepsKeys[kind], Module: "assistant", Type: settings.Int, Default: "24",
			Check: settings.Between(1, 200), Class: settings.Advanced, Scopes: instance,
		})
	}
	settings.MustRegister(defs...)
}

func maxStepsKey(kind tool.Kind) string {
	if kind == tool.Sub {
		return assistant.KeySubagentMaxSteps
	}
	return maxStepsKeys[kind]
}

func maxSteps(ctx context.Context, q db.Querier, kind tool.Kind) (int, error) {
	return intSetting(ctx, q, maxStepsKey(kind))
}

func doomLoopThreshold(ctx context.Context, q db.Querier) (int, error) {
	return intSetting(ctx, q, DoomLoopThresholdKey)
}

func intSetting(ctx context.Context, q db.Querier, key string) (int, error) {
	found, err := settings.Default.Resolve(ctx, q, key, 0)
	if err != nil {
		return 0, err
	}
	n, err := found.Int()
	return int(n), err
}

func forcedToolChoice(choice *string, model pricing.Model, tools map[string]tool.Tool) (*anthropic.ToolChoice, error) {
	if choice == nil || *choice == "" || *choice == "auto" {
		return nil, nil
	}
	out := &anthropic.ToolChoice{Type: anthropic.ToolChoiceAny}
	if *choice != anthropic.ToolChoiceAny {
		if _, ok := tools[*choice]; !ok {
			return nil, RunLimit(fmt.Sprintf("tool_choice names %q, which is not available to this run", *choice))
		}
		out = &anthropic.ToolChoice{Type: anthropic.ToolChoiceTool, Name: *choice}
	}
	if !model.ForcedTools {
		log.Info(fmt.Sprintf("%s does not accept a forced tool_choice; sending the request without it", model.ID))
		return nil, nil
	}
	return out, nil
}
