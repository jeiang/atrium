package webfetch

import (
	"mime"
	"net/http"
	"strings"
)

func contentCharset(h http.Header) string {
	value := h.Get("Content-Type")
	if value == "" {
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
