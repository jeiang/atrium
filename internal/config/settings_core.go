package config

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyTZ                     = "tz"
	KeyPublicURL              = "public_url"
	InitialTZEnv              = "ATRIUM_INITIAL_TZ"
	KeyLoopsWaitingAfterDays  = "openloops_waiting_after_days"
	KeyLoopsRemindAheadDays   = "openloops_remind_ahead_days"
	KeyLoopsRemindHour        = "openloops_remind_hour"
	KeyLoopsSnoozeDefaultDays = "openloops_snooze_default_days"
	coreModule                = "core"
)

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: KeyPublicURL, Module: coreModule, Type: settings.String, Check: checkPublicURL},
	}
}

func Settings() []settings.Def {
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{
			Key: KeyTZ, Label: "Time zone", Module: coreModule, Type: settings.String, Default: Timezone,
			Help: "Sets the day and month boundaries for schedules, calendar days and work hours, open loop reminders, package and mail reports, notifications, " +
				"the Obsidian export and the attention view. Those read the zone once at start, so restart atrium to apply a change; " +
				"budget months and classification counts read it live.",
			Restart: true, Check: checkZone}),
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
