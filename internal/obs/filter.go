package obs

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/jeiang/atrium/internal/logx"
)

const maxAttributeValue = 256

type filterExporter struct {
	next sdktrace.SpanExporter
}

func Filter(next sdktrace.SpanExporter) sdktrace.SpanExporter {
	return filterExporter{next: next}
}

func (e filterExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		out[i] = filteredSpan{ReadOnlySpan: span}
	}
	return e.next.ExportSpans(ctx, out)
}

func (e filterExporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

type filteredSpan struct {
	sdktrace.ReadOnlySpan
}

func (s filteredSpan) Attributes() []attribute.KeyValue {
	attrs := s.ReadOnlySpan.Attributes()
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		if _, ok := allowed[string(a.Key)]; !ok || logx.IsSecret(string(a.Key)) {
			continue
		}
		if a.Value.Type() == attribute.STRING {
			value := logx.Scrub(a.Value.AsString())
			if len(value) > maxAttributeValue {
				value = value[:maxAttributeValue]
			}
			a = attribute.String(string(a.Key), value)
		}
		out = append(out, a)
	}
	return out
}

func (s filteredSpan) Events() []sdktrace.Event { return nil }

func (s filteredSpan) Links() []sdktrace.Link { return nil }
