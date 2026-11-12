package observability

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/settings"
)

const TracesEndpointKey = "otel_traces_endpoint"

func init() {
	settings.MustRegisterHost(settings.HostDef{
		Key: TracesEndpointKey, Module: "core", Type: settings.String, Default: "", Check: checkEndpoint,
	})
}

func checkEndpoint(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return settings.Invalid("must be an http(s) URL including the OTLP traces path, or empty to disable tracing")
	}
	return nil
}

func Start(ctx context.Context, env config.Env) (func(context.Context), error) {
	endpoint := config.From(ctx).HostValue(TracesEndpointKey)
	if endpoint == "" || disabled(env) {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) {}, nil
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp traces exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(obs.Filter(exporter)),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName("atrium"))),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit: 256,
			AttributeCountLimit:       64,
			EventCountLimit:           0,
			LinkCountLimit:            0,
		}),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return func(ctx context.Context) {
		shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = provider.Shutdown(shutdown)
		otel.SetTracerProvider(noop.NewTracerProvider())
	}, nil
}

func disabled(env config.Env) bool {
	if env == nil {
		return false
	}
	value, ok := env("OTEL_SDK_DISABLED")
	return ok && strings.EqualFold(strings.TrimSpace(value), "true")
}
