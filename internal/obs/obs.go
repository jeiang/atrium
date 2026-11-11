package obs

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const scope = "github.com/jeiang/atrium"

const (
	HTTPMethod             = "http.request.method"
	HTTPRoute              = "http.route"
	HTTPStatus             = "http.response.status_code"
	HTTPResponseSize       = "http.response.body.size"
	HTTPRequestBodySize    = "http.request.body.size"
	URLScheme              = "url.scheme"
	ServerAddress          = "server.address"
	ServerPort             = "server.port"
	NetworkProtocolVersion = "network.protocol.version"

	RunID        = "run.id"
	ThreadID     = "thread.id"
	RunKind      = "run.kind"
	RunState     = "run.state"
	RunEffort    = "run.effort"
	RunStepCount = "run.steps"
	StepIndex    = "step.index"
	ContextSize  = "context.tokens"
	RunCostUSD   = "run.cost_usd"

	GenAIRequestModel    = "gen_ai.request.model"
	GenAIInputTokens     = "gen_ai.usage.input_tokens"
	GenAIOutputTokens    = "gen_ai.usage.output_tokens"
	GenAICacheRead       = "gen_ai.usage.cache_read.input_tokens"
	GenAICacheCreation   = "gen_ai.usage.cache_creation.input_tokens"
	GenAIStopReason      = "gen_ai.response.stop_reason"
	LLMWallMS            = "llm.wall_ms"
	LLMCostUSD           = "llm.cost_usd"
	LLMPartial           = "llm.partial"
	LLMCacheHitRatio     = "llm.cache_hit_ratio"
	ToolName             = "tool.name"
	ToolEffect           = "tool.effect"
	ToolOK               = "tool.ok"
	ToolDurationMS       = "tool.duration_ms"
	ErrorType            = "error.type"
	MailAccount          = "mail.account"
	MailNewCount         = "mail.new"
	MailGoneCount        = "mail.gone"
	MailFlagChangesCount = "mail.flag_changes"
	MailMailboxes        = "mail.mailboxes"
	MailboxRole          = "mailbox.role"
	NotifyChannel        = "notify.channel"
	NotifyBackend        = "notify.backend"
	NotifyDelivered      = "notify.delivered"
	EmbedModel           = "embed.model"
	EmbedBatchSize       = "embed.batch_size"
	EmbedDimensions      = "embed.dimensions"
	EmbedOK              = "embed.ok"
	DBOperation          = "db.operation"
	JobName              = "job.name"
	JobStatus            = "job.status"
)

var allowed = map[string]struct{}{
	HTTPMethod: {}, HTTPRoute: {}, HTTPStatus: {}, HTTPResponseSize: {}, HTTPRequestBodySize: {},
	URLScheme: {}, ServerAddress: {}, ServerPort: {}, NetworkProtocolVersion: {},
	"http.method": {}, "http.status_code": {}, "http.scheme": {}, "http.host": {}, "http.flavor": {},
	"net.host.name": {}, "net.host.port": {}, "http.server_name": {},
	RunID: {}, ThreadID: {}, RunKind: {}, RunState: {}, RunEffort: {}, RunStepCount: {}, StepIndex: {},
	ContextSize: {}, RunCostUSD: {},
	GenAIRequestModel: {}, GenAIInputTokens: {}, GenAIOutputTokens: {}, GenAICacheRead: {},
	GenAICacheCreation: {}, GenAIStopReason: {}, LLMWallMS: {}, LLMCostUSD: {}, LLMPartial: {},
	LLMCacheHitRatio: {}, ToolName: {}, ToolEffect: {}, ToolOK: {}, ToolDurationMS: {}, ErrorType: {},
	MailAccount: {}, MailNewCount: {}, MailGoneCount: {}, MailFlagChangesCount: {}, MailMailboxes: {}, MailboxRole: {},
	NotifyChannel: {}, NotifyBackend: {}, NotifyDelivered: {},
	EmbedModel: {}, EmbedBatchSize: {}, EmbedDimensions: {}, EmbedOK: {}, DBOperation: {},
	JobName: {}, JobStatus: {},
}

func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer(scope).Start(ctx, name, opts...)
}

func Fail(span trace.Span, class string) {
	if class == "" {
		class = "error"
	}
	span.SetStatus(codes.Error, class)
}

func String(key, value string) attribute.KeyValue { return attribute.String(key, value) }

func Int(key string, value int) attribute.KeyValue { return attribute.Int(key, value) }

func Int64(key string, value int64) attribute.KeyValue { return attribute.Int64(key, value) }

func Float(key string, value float64) attribute.KeyValue { return attribute.Float64(key, value) }

func Bool(key string, value bool) attribute.KeyValue { return attribute.Bool(key, value) }
