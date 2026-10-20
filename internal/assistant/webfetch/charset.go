package webfetch

import "github.com/jeiang/atrium/internal/mail/mime"

func contentCharset(h headers) string {
	value, ok := h.first("content-type")
	if !ok {
		return "utf-8"
	}
	if charset, ok := mime.ContentCharset(value); ok && charset != "" {
		return charset
	}
	return "utf-8"
}
