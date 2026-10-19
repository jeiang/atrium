package tpl

import (
	"fmt"
	"html/template"
	"strings"
)

func safeURL(v any) template.URL {
	raw := str(v)
	if strings.HasPrefix(raw, "//") {
		return "#"
	}
	scheme := ""
	if i := strings.IndexAny(raw, ":/?#"); i > 0 && raw[i] == ':' {
		scheme = raw[:i]
	}
	switch strings.ToLower(scheme) {
	case "", "http", "https", "mailto", "tel", "notion":
		return template.URL(raw)
	}
	return "#"
}

func pct(v any) (string, error) {
	f, err := toFloat(v)
	return fmt.Sprintf("%.0f%%", f*100), err
}

func place(v any) string {
	switch s := str(v); {
	case v == nil:
		return "Gone"
	case s == "INBOX":
		return "Inbox"
	default:
		return strings.ReplaceAll(s, "|", " · ")
	}
}

func proposal(action, dest any) string {
	switch {
	case str(action) == "delete":
		return "Delete"
	case str(action) == "none" || !truthy(dest):
		return "Leave in inbox"
	}
	return str(dest)
}

func noun(account any) string {
	if str(account) == "gmail" {
		return "label"
	}
	return "folder"
}

func sender(item any) string {
	for _, key := range []string{"from_name", "from_addr"} {
		if v := opt(item, key, nil); truthy(v) {
			return str(v)
		}
	}
	return "Unknown sender"
}

func pagePath(path any) string {
	var titles []string
	for _, crumb := range elems(path) {
		titles = append(titles, str(crumb["title"]))
	}
	return strings.Join(titles, " / ")
}
