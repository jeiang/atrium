package application

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/weather"
)

type Querier = db.Querier

type ThreadKind = threads.Kind

const (
	ChatThread     = threads.Chat
	SubagentThread = threads.Subagent
)

var ErrAllBusy = scheduler.ErrAllBusy

func (s *Service) RunDict(ctx context.Context, runID int64) (map[string]any, error) {
	return service.RunDict(ctx, s.db.Read, runID)
}

const EffortMedium = pricing.Medium

var EffortChoices = pricing.Efforts

const (
	WeatherForecastDays = weather.TuningWeatherForecastDays
	WeatherHourlyHours  = weather.TuningWeatherHourlyHours
)

const LoopResearch = loop.Research

const SubagentContextIDsLimit = assistant.TuningAgentSubagentContextIDs

type EnabledSubagent = subagents.Agent

func EnabledSubagents(ctx context.Context, q db.Querier) ([]EnabledSubagent, error) {
	return subagents.Enabled(ctx, q)
}

type ProposalDefinition = proposals.Definition

type ProposalBuilder = proposals.Builder

func ApprovalGate(t tool.Tool) tool.Tool { return approval.Gate(t) }

type ReportField = report.Field

type ReportType = report.Type

const (
	ReportNumber  = report.Number
	ReportDate    = report.Date
	ReportBoolean = report.Boolean
	ReportEnum    = report.Enum
	ReportID      = report.ID
	ReportText    = report.Text
)
