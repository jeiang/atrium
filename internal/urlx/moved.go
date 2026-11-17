package urlx

import (
	"net/http"
	"net/url"
	"strings"
)

const LegacyBase = "/next"

type move struct {
	from []string
	to   string
}

var moves = compileMoves([][2]string{
	{"/review", "/inbox"},
	{"/activity", "/inbox"},
	{"/drafts", "/inbox"},
	{"/triage", "/apps/mail/decisions"},
	{"/rules", "/apps/mail/rules"},
	{"/rules/new", "/apps/mail/rules"},
	{"/rules/{id}/edit", "/apps/mail/rules/{id}"},
	{"/live", "/apps/mail/mode"},
	{"/messages/{id}", "/apps/mail/message/{id}"},
	{"/messages/{id}/correct", "/apps/mail/message/{id}"},
	{"/watches", "/apps/packages"},
	{"/loops", "/plan/waiting"},
	{"/today", "/"},
	{"/chat/{id}/work", "/chat/{id}"},
	{"/chat/{id}/history", "/chat/{id}"},
	{"/chat/{id}/runs/{run}", "/chat/{id}"},
	{"/projects", "/knowledge/spaces"},
	{"/projects/new", "/knowledge/spaces"},
	{"/projects/{id}/edit", "/knowledge/spaces/{id}"},
	{"/projects/{id}/files", "/knowledge/spaces/{id}"},
	{"/library/files", "/knowledge/files"},
	{"/library/knowledge", "/knowledge"},
	{"/library/knowledge/profile", "/knowledge/me"},
	{"/library/knowledge/new", "/knowledge"},
	{"/library/knowledge/{id}", "/knowledge/{id}"},
	{"/scheduled", "/assistant/routines"},
	{"/settings/auth", "/settings/sign-in"},
	{"/settings/assistant", "/assistant/behavior"},
	{"/settings/permissions", "/assistant/authority"},
	{"/settings/usage", "/assistant/spend"},
	{"/settings/export", "/settings/knowledge-export"},
	{"/settings/media", "/settings/library"},
	{"/settings/packages", "/settings/apps"},
	{"/settings/weather", "/settings/connections"},
	{"/settings/connections/google", GoogleSetup},
	{GoogleCallback, GoogleSetup},
	{"/apps/media/downloads", "/apps/torrents"},
	{"/apps/media/torrents", "/apps/torrents"},
	{"/apps/media/add", "/apps/torrents"},
	{"/apps/media/torrents/{hash}/files", "/apps/torrents"},
	{"/apps/media/channels", "/apps/media/following"},
	{"/apps/media/library", "/apps/media"},
	{"/apps/media/library/movie/{name}", "/apps/media"},
	{"/apps/media/library/tv/{name}", "/apps/media"},
})

const (
	GoogleSetup    = "/settings/connections/google/default"
	GoogleCallback = "/settings/connections/google/callback"
)

func compileMoves(pairs [][2]string) []move {
	out := make([]move, len(pairs))
	for i, pair := range pairs {
		out[i] = move{from: strings.Split(pair[0], "/"), to: pair[1]}
	}
	return out
}

func Moved(escapedPath, rawQuery string) (string, int, bool) {
	segments := strings.Split(escapedPath, "/")
	for _, m := range moves {
		params, ok := match(m.from, segments)
		if !ok {
			continue
		}
		target := m.to
		for name, value := range params {
			target = strings.ReplaceAll(target, "{"+name+"}", value)
		}
		status := http.StatusMovedPermanently
		switch {
		case m.to == GoogleSetup && escapedPath == GoogleCallback:
			status = http.StatusSeeOther
		case escapedPath == "/library/knowledge" && unconfirmed(rawQuery):
			return "/knowledge/review", status, true
		}
		if rawQuery != "" {
			target += "?" + rawQuery
		}
		return target, status, true
	}
	return "", 0, false
}

func match(pattern, segments []string) (map[string]string, bool) {
	if len(pattern) != len(segments) {
		return nil, false
	}
	var params map[string]string
	for i, part := range pattern {
		if name, ok := strings.CutPrefix(part, "{"); ok {
			if segments[i] == "" {
				return nil, false
			}
			if params == nil {
				params = map[string]string{}
			}
			params[strings.TrimSuffix(name, "}")] = segments[i]
			continue
		}
		if part != segments[i] {
			return nil, false
		}
	}
	return params, true
}

func unconfirmed(rawQuery string) bool {
	values, err := url.ParseQuery(rawQuery)
	return err == nil && values.Get("state") == "unconfirmed"
}

func StripLegacy(path string) (string, bool) {
	if path != LegacyBase && !strings.HasPrefix(path, LegacyBase+"/") {
		return path, false
	}
	return "/" + strings.TrimLeft(strings.TrimPrefix(path, LegacyBase), "/\\"), true
}
