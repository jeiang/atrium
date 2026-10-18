package logx

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"

	slogjournal "github.com/systemd/slog-journal"
)

const (
	Component = "COMPONENT"
	RunID     = "RUN_ID"
	ThreadID  = "THREAD_ID"
	Job       = "JOB"
	Account   = "ACCOUNT"
	Err       = "ERR"
)

const Redacted = "[redacted]"

func Setup(stderr io.Writer) {
	slog.SetDefault(slog.New(NewHandler(stderr)))
}

func NewHandler(stderr io.Writer) slog.Handler {
	if isJournalStream(stderr) {
		h, err := slogjournal.NewHandler(&slogjournal.Options{
			Level:        slog.LevelInfo,
			ReplaceAttr:  replaceAttr,
			ReplaceGroup: Key,
		})
		if err == nil {
			return h
		}
	}
	return newTextHandler(stderr, slog.LevelInfo)
}

func Logger(component string) *slog.Logger {
	return slog.New(deferred{}.WithAttrs([]slog.Attr{slog.String(Component, component)}))
}

func Key(name string) string {
	b := []byte(strings.ToUpper(name))
	for i, c := range b {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			b[i] = '_'
		}
	}
	if len(b) == 0 || (b[0] >= '0' && b[0] <= '9') {
		return "_" + string(b)
	}
	return string(b)
}

func IsSecret(key string) bool {
	k := Key(key)
	return strings.Contains(k, "PASSWORD") || strings.Contains(k, "SECRET") || strings.Contains(k, "TOKEN") ||
		k == "KEY" || strings.HasSuffix(k, "_KEY") || strings.HasSuffix(k, "APIKEY")
}

func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	a.Key = Key(a.Key)
	if IsSecret(a.Key) {
		a.Value = slog.StringValue(Redacted)
	}
	return a
}

type deferred struct {
	ops []func(slog.Handler) slog.Handler
}

func (d deferred) with(op func(slog.Handler) slog.Handler) deferred {
	return deferred{append(slices.Clip(d.ops), op)}
}

func (d deferred) Enabled(ctx context.Context, l slog.Level) bool {
	return slog.Default().Handler().Enabled(ctx, l)
}

func (d deferred) Handle(ctx context.Context, r slog.Record) error {
	h := slog.Default().Handler()
	for _, op := range d.ops {
		h = op(h)
	}
	return h.Handle(ctx, r)
}

func (d deferred) WithAttrs(attrs []slog.Attr) slog.Handler {
	return d.with(func(h slog.Handler) slog.Handler { return h.WithAttrs(attrs) })
}

func (d deferred) WithGroup(name string) slog.Handler {
	if name == "" {
		return d
	}
	return d.with(func(h slog.Handler) slog.Handler { return h.WithGroup(name) })
}
