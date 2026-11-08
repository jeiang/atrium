package oidc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
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
	RedirectURI   string
}

type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	return "oidc is not configured: " + strings.Join(e.Problems, "; ")
}

func list(value string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

var valueKeys = append([]string{KeyPreset}, append(wizardKeys, KeyIdleDays, KeyMaxDays, KeyRecheckMinutes)...)

type values map[string]string

func resolved(ctx context.Context, q db.Querier) (values, []string, error) {
	out := values{}
	for _, key := range valueKeys {
		found, err := settings.Default.Resolve(ctx, q, key)
		if err != nil {
			return nil, nil, err
		}
		out[key] = strings.TrimSpace(found.Value)
	}
	return out, nil, nil
}

func load(ctx context.Context, q db.Querier, env config.Env, encrypted bool) (Config, error) {
	vals, problems, err := resolved(ctx, q)
	if err != nil {
		return Config{}, err
	}
	secret, err := secrets.Credential(ctx, q, secrets.OIDCClientSecret)
	if err != nil {
		var empty settings.EmptyValue
		if !errors.As(err, &empty) {
			return Config{}, err
		}
		problems = append(problems, err.Error())
	}
	return assemble(vals, strings.TrimSpace(secret), env, encrypted, problems)
}

func assemble(get values, secret string, env config.Env, encrypted bool, problems []string) (Config, error) {
	days := func(key string, unit time.Duration, low, high int64) time.Duration {
		n, err := strconv.ParseInt(strings.TrimSpace(get[key]), 10, 64)
		switch {
		case err != nil:
			problems = append(problems, key+" is not a number")
		case n < low || n > high:
			problems = append(problems, fmt.Sprintf("%s must be between %d and %d", key, low, high))
		}
		return time.Duration(n) * unit
	}
	name := get[KeyPreset]
	preset, ok := presetNamed(name)
	if !ok {
		problems = append(problems, "unknown preset "+strconv.Quote(name))
	}
	c := Config{
		Preset:        preset,
		Issuer:        get[KeyIssuer],
		ClientID:      get[KeyClientID],
		Scopes:        list(get[KeyScopes]),
		GroupsClaim:   get[KeyGroupsClaim],
		AccessGroup:   get[KeyAccessGroup],
		AdminGroup:    get[KeyAdminGroup],
		AccessEmails:  list(strings.ToLower(get[KeyAccessEmails])),
		AdminEmails:   list(strings.ToLower(get[KeyAdminEmails])),
		AccessDomains: list(strings.ToLower(get[KeyAccessDomains])),
		AdminDomains:  list(strings.ToLower(get[KeyAdminDomains])),
		Idle:          days(KeyIdleDays, 24*time.Hour, 1, 365),
		Max:           days(KeyMaxDays, 24*time.Hour, 1, 365),
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
	c.ClientSecret = secret
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
	}
	if c.GroupsClaim == "" && len(c.AccessEmails) == 0 && len(c.AdminEmails) == 0 && len(c.AccessDomains) == 0 && len(c.AdminDomains) == 0 {
		problems = append(problems, fmt.Sprintf("nothing grants access: set %s, %s or %s", KeyGroupsClaim, KeyAccessEmails, KeyAccessDomains))
	}
	if !slices.Contains(c.Scopes, "openid") {
		problems = append(problems, KeyScopes+" must include openid")
	}
	if c.GroupsClaim != "" && c.ClientSecret == "" && !encrypted {
		problems = append(problems, "group re-checks keep refresh tokens, which needs a client secret or an encrypted database")
	}
	if len(problems) > 0 {
		return c, &ConfigError{Problems: problems}
	}
	return c, nil
}

func AdminGroup(ctx context.Context, q db.Querier) (string, error) {
	found, err := settings.Default.Resolve(ctx, q, KeyAdminGroup)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(found.Value) == "" {
		return DefaultAdminGroup, nil
	}
	return strings.TrimSpace(found.Value), nil
}
