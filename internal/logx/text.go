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
	n.pre = appendFields(n.pre, flattenAll(h.prefix, attrs), &n.component)
	return &n
}

func (h *textHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.prefix += sanitize(name) + "_"
	return &n
}

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	name := h.component
	flat := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		flat = flatten(flat, h.prefix, a)
		return true
	})
	attrs := appendFields(nil, flat, &name)
	t := r.Time.In(h.loc)
	message := Scrub(r.Message)
	buf := make([]byte, 0, 128+len(message)+len(h.pre)+len(attrs))
	buf = t.AppendFormat(buf, "2006-01-02 15:04:05,")
	ms := t.Nanosecond() / int(time.Millisecond)
	buf = append(buf, byte('0'+ms/100), byte('0'+ms/10%10), byte('0'+ms%10), ' ')
	buf = append(buf, name...)
	buf = append(buf, ' ')
	buf = append(buf, levelName(r.Level)...)
	buf = append(buf, ' ')
	buf = append(buf, message...)
	buf = append(buf, h.pre...)
	buf = append(buf, attrs...)
	buf = append(buf, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func appendFields(b []byte, attrs []slog.Attr, component *string) []byte {
	for _, a := range attrs {
		v := a.Value.String()
		if a.Key == Component {
			*component = v
			continue
		}
		b = append(b, ' ')
		b = append(b, a.Key...)
		b = append(b, '=')
		if needsQuoting(v) {
			b = strconv.AppendQuote(b, v)
		} else {
			b = append(b, v...)
		}
	}
	return b
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
