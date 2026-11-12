package config

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyTZ                = "tz"
	KeyPublicURL         = "public_url"
	KeySnoozeChoices     = "snooze_choices"
	InitialTZEnv         = "ATRIUM_INITIAL_TZ"
	coreModule           = "core"
	webModule            = "web"
	defaultSnoozeChoices = "1,3,7,14"
)

func init() {
	settings.MustRegisterHost(HostSettings()...)
	settings.MustRegister(Settings()...)
}

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: KeyPublicURL, Module: coreModule, Type: settings.String, Check: checkPublicURL},
	}
}

func Settings() []settings.Def {
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{
			Key: KeyTZ, Module: coreModule, Type: settings.String, Default: Timezone,
			Check: checkZone}),
		settings.Value(settings.UI, settings.Def{
			Key: KeySnoozeChoices, Label: "Snooze choices (days)", Module: webModule, Type: settings.String, Default: defaultSnoozeChoices,
			Check: checkSnoozeChoices}),
	}
}

func checkZone(value string) error {
	if _, err := Zone(value); err != nil {
		return settings.Invalid("must be an IANA time zone name such as " + Timezone)
	}
	return nil
}

func checkPublicURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return settings.Invalid("must be an http or https URL")
	}
	return nil
}

func snoozeDays(value string) ([]int, bool) {
	var days []int
	for _, part := range strings.Split(value, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
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
	tz string
}

func loadCore(ctx context.Context, q db.Querier) (core, error) {
	c := core{}
	for _, field := range []struct {
		key  string
		into func(value string) error
	}{
		{KeyTZ, func(v string) error { c.tz = strings.TrimSpace(v); return nil }},
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

func (s *State) TZName(env Env) string {
	s.mu.RLock()
	c := s.core
	s.mu.RUnlock()
	if c.tz != "" {
		return c.tz
	}
	return Timezone
}

func (s *State) PublicURL(env Env) string {
	return strings.TrimRight(s.HostValue(KeyPublicURL), "/")
}

func (s *State) HTTPTimeout(env Env) time.Duration {
	return TuningHttpTimeoutSeconds * time.Second
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
