package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
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

func (s *Service) load(ctx context.Context) (Config, error) {
	conn, err := s.open(ctx)
	if err != nil {
		return Config{}, err
	}
	defer conn.Close()
	return s.assemble(ctx, conn)
}

func (s *Service) assemble(ctx context.Context, conn Connection) (Config, error) {
	vals, err := ParseConnectionConfig(rawConfig(conn.Values()))
	if err != nil {
		return Config{}, err
	}
	var problems []string
	days := func(key string, unit time.Duration, low, high int64) time.Duration {
		found, err := settings.From(ctx).Resolve(ctx, s.d.Read, key)
		if err != nil {
			problems = append(problems, err.Error())
			return 0
		}
		n, err := found.Int()
		switch {
		case err != nil:
			problems = append(problems, key+" is not a number")
		case n < low || n > high:
			problems = append(problems, fmt.Sprintf("%s must be between %d and %d", key, low, high))
		}
		return time.Duration(n) * unit
	}
	name := vals.Preset
	preset, ok := presetNamed(name)
	if !ok {
		problems = append(problems, "unknown preset "+strconv.Quote(name))
	}
	secret, err := conn.Credential(cfgClientSecret)
	if err != nil {
		if !errors.Is(err, ErrNoCredential) {
			return Config{}, err
		}
		secret = ""
	}
	c := Config{
		Preset:        preset,
		Issuer:        strings.TrimSpace(vals.Issuer),
		ClientID:      strings.TrimSpace(vals.ClientID),
		Scopes:        list(vals.Scopes),
		GroupsClaim:   strings.TrimSpace(vals.GroupsClaim),
		AccessGroup:   strings.TrimSpace(vals.AccessGroup),
		AdminGroup:    strings.TrimSpace(vals.AdminGroup),
		AccessEmails:  list(strings.ToLower(vals.AccessEmails)),
		AdminEmails:   list(strings.ToLower(vals.AdminEmails)),
		AccessDomains: list(strings.ToLower(vals.AccessDomains)),
		AdminDomains:  list(strings.ToLower(vals.AdminDomains)),
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
	public := config.From(ctx).PublicURL(s.env)
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
	if c.GroupsClaim != "" && c.ClientSecret == "" && !s.d.Encrypted() {
		problems = append(problems, "group re-checks keep refresh tokens, which needs a client secret or an encrypted database")
	}
	if len(problems) > 0 {
		return c, &ConfigError{Problems: problems}
	}
	return c, nil
}

func (s *Service) AdminGroup(ctx context.Context) (string, error) {
	conn, err := s.open(ctx)
	if err != nil {
		return DefaultAdminGroup, nil
	}
	defer conn.Close()
	vals, err := ParseConnectionConfig(rawConfig(conn.Values()))
	if err != nil {
		return DefaultAdminGroup, nil
	}
	if group := strings.TrimSpace(vals.AdminGroup); group != "" {
		return group, nil
	}
	return DefaultAdminGroup, nil
}

func rawConfig(values map[string]string) json.RawMessage {
	raw, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	return raw
}
