package webfetch

import (
	"mime"
	"strings"
)

func contentCharset(h headers) string {
	value, ok := h.first("content-type")
	if !ok {
		return "utf-8"
	}
	_, params, err := mime.ParseMediaType(value)
	if err != nil {
		return "utf-8"
	}
	if charset := strings.TrimSpace(params["charset"]); charset != "" {
		return charset
	}
	return "utf-8"
}
