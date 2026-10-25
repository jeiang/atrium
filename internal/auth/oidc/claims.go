package oidc

import (
	"slices"
	"strings"
)

type identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Groups        []string
	HasGroups     bool
}

func claimString(claims map[string]any, key string) string {
	s, _ := claims[key].(string)
	s = strings.TrimSpace(s)
	if len(s) > 255 {
		s = s[:255]
	}
	return s
}

func claimBool(claims map[string]any, key string) bool {
	switch v := claims[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

func lookupClaim(claims map[string]any, path string) (any, bool) {
	if v, ok := claims[path]; ok {
		return v, true
	}
	var cur any = claims
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func groupList(v any, trimSlash bool) ([]string, bool) {
	var raw []string
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			raw = append(raw, s)
		}
	case string:
		raw = strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ' ' })
	default:
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, g := range raw {
		g = strings.TrimSpace(g)
		if trimSlash {
			g = strings.TrimPrefix(g, "/")
		}
		if g != "" {
			out = append(out, g)
		}
	}
	return out, true
}

func identityOf(claims map[string]any, cfg Config) identity {
	id := identity{
		Subject:       claimString(claims, "sub"),
		Email:         strings.ToLower(claimString(claims, "email")),
		EmailVerified: claimBool(claims, "email_verified"),
		Name:          claimString(claims, "name"),
	}
	if id.Name == "" {
		id.Name = claimString(claims, "preferred_username")
	}
	if cfg.GroupsClaim != "" {
		if v, ok := lookupClaim(claims, cfg.GroupsClaim); ok {
			id.Groups, id.HasGroups = groupList(v, cfg.Preset.TrimSlash)
		}
	}
	return id
}

func (c Config) decide(id identity) (access, admin bool) {
	emailOK := id.EmailVerified && id.Email != ""
	domain := id.Email[strings.LastIndexByte(id.Email, '@')+1:]
	admin = slices.Contains(id.Groups, c.AdminGroup) || (emailOK && (slices.Contains(c.AdminEmails, id.Email) || slices.Contains(c.AdminDomains, domain)))
	access = admin || slices.Contains(id.Groups, c.AccessGroup) || (emailOK && (slices.Contains(c.AccessEmails, id.Email) || slices.Contains(c.AccessDomains, domain)))
	return access, admin
}
