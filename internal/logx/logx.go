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
	Component    = "COMPONENT"
	RunID        = "RUN_ID"
	ThreadID     = "THREAD_ID"
	Job          = "JOB"
	Account      = "ACCOUNT"
	Err          = "ERR"
	Module       = "MODULE"
	State        = "STATE"
	Reason       = "REASON"
	Command      = "COMMAND"
	EnvName      = "ENV_NAME"
	UseInstead   = "USE_INSTEAD"
	Model        = "MODEL"
	SettingKey   = "SETTING"
	SettingValue = "VALUE"
)

const Redacted = "[redacted]"

func Setup(stderr io.Writer) {
	slog.SetDefault(slog.New(NewHandler(stderr)))
}

func NewHandler(stderr io.Writer) slog.Handler {
	if isJournalStream(stderr) {
		if h, err := slogjournal.NewHandler(&slogjournal.Options{Level: slog.LevelInfo}); err == nil {
			return &flatHandler{inner: h}
		}
	}
	return newTextHandler(stderr, slog.LevelInfo)
}

func Logger(component string) *slog.Logger {
	return slog.New(deferred{}.WithAttrs([]slog.Attr{slog.String(Component, component)}))
}

func sanitize(name string) string {
	b := []byte(strings.ToUpper(name))
	for i, c := range b {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			b[i] = '_'
		}
	}
	return string(b)
}

var journalFields = [...]string{"MESSAGE", "MESSAGE_ID", "PRIORITY", "ERRNO", "TID", "INVOCATION_ID", "USER_INVOCATION_ID", "DOCUMENTATION"}

func Key(name string) string {
	k := sanitize(name)
	switch {
	case k == "":
		return "F_"
	case k[0] == '_':
		return "F" + k
	case k[0] >= '0' && k[0] <= '9',
		slices.Contains(journalFields[:], k),
		strings.HasPrefix(k, "CODE_"),
		strings.HasPrefix(k, "SYSLOG_"):
		return "F_" + k
	}
	return k
}

var secretWords = [...]string{"PASSWORD", "PASSWD", "SECRET", "PRIVATEKEY", "AUTHORIZATION", "COOKIE", "CREDENTIAL"}

func IsSecret(field string) bool {
	k := sanitize(field)
	for _, w := range secretWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	for seg := range strings.SplitSeq(k, "_") {
		if seg == "KEY" || strings.HasSuffix(seg, "APIKEY") || (strings.Contains(seg, "TOKEN") && !strings.HasSuffix(seg, "TOKENS")) {
			return true
		}
	}
	return false
}

func flatten(dst []slog.Attr, prefix string, a slog.Attr) []slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return dst
	}
	if a.Value.Kind() != slog.KindGroup {
		key := Key(prefix + a.Key)
		if IsSecret(key) {
			a.Value = slog.StringValue(Redacted)
		} else if k := a.Value.Kind(); (k == slog.KindString || k == slog.KindAny) && active() {
			text := a.Value.String()
			if scrubbed := Scrub(text); scrubbed != text {
				a.Value = slog.StringValue(scrubbed)
			}
		}
		return append(dst, slog.Attr{Key: key, Value: a.Value})
	}
	attrs := a.Value.Group()
	if len(attrs) == 0 {
		return dst
	}
	if a.Key != "" {
		if IsSecret(prefix + a.Key) {
			return append(dst, slog.String(Key(prefix+a.Key), Redacted))
		}
		prefix += sanitize(a.Key) + "_"
	}
	for _, g := range attrs {
		dst = flatten(dst, prefix, g)
	}
	return dst
}

func flattenAll(prefix string, attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out = flatten(out, prefix, a)
	}
	return out
}

type flatHandler struct {
	inner  slog.Handler
	prefix string
}

func (h *flatHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *flatHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Scrub(r.Message), r.PC)
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = flatten(attrs, h.prefix, a)
		return true
	})
	out.AddAttrs(attrs...)
	return h.inner.Handle(ctx, out)
}

func (h *flatHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &flatHandler{inner: h.inner.WithAttrs(flattenAll(h.prefix, attrs)), prefix: h.prefix}
}

func (h *flatHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &flatHandler{inner: h.inner, prefix: h.prefix + sanitize(name) + "_"}
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
