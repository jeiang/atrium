package loop

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

func maxStepsKey(kind tool.Kind) string {
	if kind == tool.Sub {
		return "agent_subagent_max_steps"
	}
	return "max_steps_" + string(kind)
}

func maxSteps(_ context.Context, _ db.Querier, kind tool.Kind) (int, error) {
	switch kind {
	case tool.Sub:
		return assistant.TuningAgentSubagentMaxSteps, nil
	case tool.Worker:
		return TuningMaxStepsWorker, nil
	case tool.Cron:
		return TuningMaxStepsCron, nil
	case tool.Nightly:
		return TuningMaxStepsNightly, nil
	}
	return TuningMaxStepsChat, nil
}

func doomLoopThreshold(_ context.Context, _ db.Querier) (int, error) {
	return TuningDoomLoopThreshold, nil
}

func forcedToolChoice(choice *string, model pricing.Model, set registry.Set) (*anthropic.ToolChoice, error) {
	if choice == nil || *choice == "" || *choice == "auto" {
		return nil, nil
	}
	out := &anthropic.ToolChoice{Type: anthropic.ToolChoiceAny}
	if *choice != anthropic.ToolChoiceAny {
		if !set.Has(*choice) {
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
