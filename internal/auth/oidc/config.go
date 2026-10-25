package oidc

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const CallbackPath = "/auth/callback"

type Config struct {
	Preset        Preset
	Issuer        string
	ClientID      string
	ClientSecret  string
	Scopes        []string
	GroupsClaim   string
	AccessGroup   string
	AdminGroup    string
	AccessEmails  []string
	AdminEmails   []string
	AccessDomains []string
	AdminDomains  []string
	Idle          time.Duration
	Max           time.Duration
	Recheck       time.Duration
	RedirectURI   string
	Secure        bool
}

type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	return "oidc is not configured: " + strings.Join(e.Problems, "; ")
}

func (c Config) rechecks() bool { return c.GroupsClaim != "" }

func list(value string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if part = py.Strip(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func load(ctx context.Context, q db.Querier, env config.Env) (Config, error) {
	var problems []string
	get := func(key string) string {
		found, err := settings.Default.Resolve(ctx, q, key, 0)
		if err != nil {
			problems = append(problems, err.Error())
		}
		return py.Strip(found.Value)
	}
	days := func(key string, unit time.Duration) time.Duration {
		n, ok := py.ParseInt(get(key))
		if !ok {
			problems = append(problems, key+" is not a number")
		}
		return time.Duration(n) * unit
	}
	name := get(KeyPreset)
	preset, ok := presetNamed(name)
	if !ok {
		problems = append(problems, "unknown preset "+py.Repr(name))
	}
	c := Config{
		Preset:        preset,
		Issuer:        get(KeyIssuer),
		ClientID:      get(KeyClientID),
		Scopes:        list(get(KeyScopes)),
		GroupsClaim:   get(KeyGroupsClaim),
		AccessGroup:   get(KeyAccessGroup),
		AdminGroup:    get(KeyAdminGroup),
		AccessEmails:  list(strings.ToLower(get(KeyAccessEmails))),
		AdminEmails:   list(strings.ToLower(get(KeyAdminEmails))),
		AccessDomains: list(strings.ToLower(get(KeyAccessDomains))),
		AdminDomains:  list(strings.ToLower(get(KeyAdminDomains))),
		Idle:          days(KeyIdleDays, 24*time.Hour),
		Max:           days(KeyMaxDays, 24*time.Hour),
		Recheck:       days(KeyRecheckMinutes, time.Minute),
	}
	if preset.Issuer != "" && c.Issuer == "" {
		c.Issuer = preset.Issuer
	}
	if len(c.Scopes) == 0 {
		c.Scopes = slices.Clone(preset.Scopes)
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = preset.GroupsClaim
	}
	secret, err := secrets.Credential(ctx, q, secrets.OIDCClientSecret)
	if err != nil {
		problems = append(problems, err.Error())
	}
	c.ClientSecret = py.Strip(secret)
	public := config.PublicURL(env)
	switch {
	case c.Issuer == "":
		problems = append(problems, KeyIssuer+" is not set")
	case checkEndpoint(c.Issuer) != nil:
		problems = append(problems, KeyIssuer+" must be an https URL")
	}
	if c.ClientID == "" {
		problems = append(problems, KeyClientID+" is not set")
	}
	if public == "" {
		problems = append(problems, config.KeyPublicURL+" is not set")
	} else {
		c.RedirectURI = public + CallbackPath
		c.Secure = strings.HasPrefix(public, "https://")
	}
	if c.GroupsClaim == "" && len(c.AccessEmails) == 0 && len(c.AdminEmails) == 0 && len(c.AccessDomains) == 0 && len(c.AdminDomains) == 0 {
		problems = append(problems, fmt.Sprintf("nothing grants access: set %s, %s or %s", KeyGroupsClaim, KeyAccessEmails, KeyAccessDomains))
	}
	if !slices.Contains(c.Scopes, "openid") {
		problems = append(problems, KeyScopes+" must include openid")
	}
	if len(problems) > 0 {
		return c, &ConfigError{Problems: problems}
	}
	return c, nil
}

func AdminGroup(ctx context.Context, q db.Querier) string {
	found, err := settings.Default.Resolve(ctx, q, KeyAdminGroup, 0)
	if err != nil || py.Strip(found.Value) == "" {
		return DefaultAdminGroup
	}
	return py.Strip(found.Value)
}
