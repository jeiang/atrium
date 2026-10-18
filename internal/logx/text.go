package logx

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const rootName = "root"

type textHandler struct {
	mu        *sync.Mutex
	w         io.Writer
	level     slog.Leveler
	loc       *time.Location
	component string
	prefix    string
	pre       []byte
}

func newTextHandler(w io.Writer, level slog.Leveler) *textHandler {
	return &textHandler{mu: &sync.Mutex{}, w: w, level: level, loc: time.Local, component: rootName}
}

func (h *textHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.pre = slices.Clip(h.pre)
	for _, a := range attrs {
		n.pre = n.appendAttr(n.pre, n.prefix, a, &n.component)
	}
	return &n
}

func (h *textHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.prefix += Key(name) + "_"
	return &n
}

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	name := h.component
	var attrs []byte
	r.Attrs(func(a slog.Attr) bool {
		attrs = h.appendAttr(attrs, h.prefix, a, &name)
		return true
	})
	t := r.Time.In(h.loc)
	buf := make([]byte, 0, 128+len(r.Message)+len(h.pre)+len(attrs))
	buf = t.AppendFormat(buf, "2006-01-02 15:04:05,")
	ms := t.Nanosecond() / int(time.Millisecond)
	buf = append(buf, byte('0'+ms/100), byte('0'+ms/10%10), byte('0'+ms%10), ' ')
	buf = append(buf, name...)
	buf = append(buf, ' ')
	buf = append(buf, levelName(r.Level)...)
	buf = append(buf, ' ')
	buf = append(buf, r.Message...)
	buf = append(buf, h.pre...)
	buf = append(buf, attrs...)
	buf = append(buf, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func (h *textHandler) appendAttr(b []byte, prefix string, a slog.Attr, component *string) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return b
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += Key(a.Key) + "_"
		}
		for _, g := range a.Value.Group() {
			b = h.appendAttr(b, prefix, g, component)
		}
		return b
	}
	a = replaceAttr(nil, a)
	if prefix == "" && a.Key == Component {
		*component = a.Value.String()
		return b
	}
	b = append(b, ' ')
	b = append(b, prefix...)
	b = append(b, a.Key...)
	b = append(b, '=')
	v := a.Value.String()
	if needsQuoting(v) {
		return strconv.AppendQuote(b, v)
	}
	return append(b, v...)
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r == utf8.RuneError || r == '"' || r == '=' || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARNING"
	case l == slog.LevelError:
		return "ERROR"
	default:
		return "CRITICAL"
	}
}
