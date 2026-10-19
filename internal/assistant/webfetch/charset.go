package webfetch

import "github.com/jeiang/atrium/internal/mail/mime"

// contentCharset is response.headers.get_content_charset() or "utf-8".
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
