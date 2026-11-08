package config

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyTZ                = "tz"
	KeyPublicURL         = "public_url"
	KeyHTTPTimeout       = "http_timeout_seconds"
	KeyBodyCap           = "body_cap_bytes"
	KeyPageSize          = "page_size"
	KeyLearnedLimit      = "learned_limit"
	KeyChatThreadLimit   = "chat_thread_limit"
	KeyProjectThreads    = "project_thread_limit"
	KeyLoopLimit         = "loop_limit"
	KeySnoozeChoices     = "snooze_choices"
	KeyPreviewBytes      = "preview_bytes"
	KeyRunHistory        = "run_history"
	KeyJobHistory        = "job_history"
	KeyActivityPageSize  = "activity_page_size"
	KeyActivitySnippet   = "activity_snippet_chars"
	coreModule           = "core"
	webModule            = "web"
	defaultHTTPTimeout   = 600
	defaultSnoozeChoices = "1,3,7,14"
)

func init() {
	advanced := func(key string, def string, lo, hi float64) settings.Def {
		return settings.Value(settings.Advanced, settings.Def{
			Key: key, Module: webModule, Type: settings.Int, Default: def,
			Check: settings.Between(lo, hi)})
	}
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{
			Key: KeyTZ, Module: coreModule, Type: settings.String, Default: Timezone,
			Check: checkZone}),
		settings.Value(settings.UI, settings.Def{
			Key: KeyPublicURL, Module: coreModule, Type: settings.String,
			Check: checkPublicURL}),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeyHTTPTimeout, Module: coreModule, Type: settings.Int, Default: strconv.Itoa(defaultHTTPTimeout),
			Check: settings.Between(1, 3600)}),
		advanced(KeyBodyCap, "1048576", 1, 1<<30),
		advanced(KeyPageSize, "50", 10, 500),
		advanced(KeyLearnedLimit, "50", 10, 500),
		advanced(KeyChatThreadLimit, "50", 1, 500),
		advanced(KeyProjectThreads, "100", 1, 500),
		advanced(KeyLoopLimit, "100", 1, 1000),
		settings.Value(settings.Advanced, settings.Def{
			Key: KeySnoozeChoices, Module: webModule, Type: settings.String, Default: defaultSnoozeChoices,
			Check: checkSnoozeChoices}),
		advanced(KeyPreviewBytes, "200000", 1000, 10_000_000),
		advanced(KeyRunHistory, "30", 1, 500),
		advanced(KeyJobHistory, "10", 1, 500),
		advanced(KeyActivityPageSize, "50", 10, 500),
		advanced(KeyActivitySnippet, "800", 80, 10_000),
	)
}

func checkZone(value string) error {
	if _, err := Zone(value); err != nil {
		return settings.Invalid("must be an IANA time zone name such as " + Timezone)
	}
	return nil
}

func checkPublicURL(value string) error {
	u, err := url.Parse(py.Strip(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return settings.Invalid("must be an http or https URL")
	}
	return nil
}

func snoozeDays(value string) ([]int, bool) {
	var days []int
	for _, part := range strings.Split(value, ",") {
		n, err := strconv.Atoi(py.Strip(part))
		if err != nil || n < 1 || n > 365 {
			return nil, false
		}
		days = append(days, n)
	}
	return days, len(days) > 0
}

func checkSnoozeChoices(value string) error {
	if _, ok := snoozeDays(value); !ok {
		return settings.Invalid("must be comma-separated day counts between 1 and 365")
	}
	return nil
}

type core struct {
	tz, publicURL string
	httpTimeout   time.Duration
}

func loadCore(ctx context.Context, q db.Querier) (core, error) {
	c := core{}
	for _, field := range []struct {
		key  string
		into func(value string) error
	}{
		{KeyTZ, func(v string) error { c.tz = py.Strip(v); return nil }},
		{KeyPublicURL, func(v string) error { c.publicURL = strings.TrimRight(py.Strip(v), "/"); return nil }},
		{KeyHTTPTimeout, func(v string) error {
			n, ok := py.ParseInt(v)
			if !ok {
				return settings.Invalid(KeyHTTPTimeout + " must be a whole number")
			}
			c.httpTimeout = time.Duration(n) * time.Second
			return nil
		}},
	} {
		found, err := settings.From(ctx).Resolve(ctx, q, field.key)
		if err != nil {
			return core{}, err
		}
		if err := field.into(found.Value); err != nil {
			return core{}, err
		}
	}
	return c, nil
}

func pinned(reg *settings.Registry, env Env, key string) (string, bool) {
	if value, _, ok := reg.Pin(key); ok {
		return value, true
	}
	if def, ok := reg.Lookup(key); ok {
		if value := stripped(env, EnvName(def)); value != "" {
			return value, true
		}
	}
	return "", false
}

func (s *State) coreValue(env Env, key string) (string, bool) {
	if value, ok := pinned(s.settings, env, key); ok {
		return value, true
	}
	s.mu.RLock()
	c := s.core
	s.mu.RUnlock()
	switch key {
	case KeyTZ:
		return c.tz, c.tz != ""
	case KeyPublicURL:
		return c.publicURL, true
	case KeyHTTPTimeout:
		if c.httpTimeout > 0 {
			return strconv.FormatInt(int64(c.httpTimeout/time.Second), 10), true
		}
	}
	return "", false
}

func (s *State) TZName(env Env) string {
	if value, ok := s.coreValue(env, KeyTZ); ok {
		return value
	}
	return Timezone
}

func (s *State) PublicURL(env Env) string {
	value, _ := s.coreValue(env, KeyPublicURL)
	return strings.TrimRight(value, "/")
}

func (s *State) HTTPTimeout(env Env) time.Duration {
	if value, ok := s.coreValue(env, KeyHTTPTimeout); ok {
		if n, ok := py.ParseInt(value); ok {
			return time.Duration(n) * time.Second
		}
	}
	return defaultHTTPTimeout * time.Second
}

func (s *State) Limit(ctx context.Context, q db.Querier, key string) (int, error) {
	found, err := s.settings.Resolve(ctx, q, key)
	if err != nil {
		return 0, err
	}
	n, err := found.Int()
	return int(n), err
}

func (s *State) SnoozeChoices(ctx context.Context, q db.Querier) ([]int, error) {
	found, err := s.settings.Resolve(ctx, q, KeySnoozeChoices)
	if err != nil {
		return nil, err
	}
	days, ok := snoozeDays(found.Value)
	if !ok {
		return nil, settings.Invalid(KeySnoozeChoices + " " + checkSnoozeChoices(found.Value).Error())
	}
	return days, nil
}
