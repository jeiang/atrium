package oidc

import (
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	module = "auth"

	KeyPreset         = "oidc_preset"
	KeyIssuer         = "oidc_issuer"
	KeyClientID       = "oidc_client_id"
	KeyScopes         = "oidc_scopes"
	KeyGroupsClaim    = "oidc_groups_claim"
	KeyAccessGroup    = "oidc_access_group"
	KeyAdminGroup     = "oidc_admin_group"
	KeyAccessEmails   = "oidc_access_emails"
	KeyAdminEmails    = "oidc_admin_emails"
	KeyIdleDays       = "oidc_session_idle_days"
	KeyMaxDays        = "oidc_session_max_days"
	KeyRecheckMinutes = "oidc_recheck_minutes"

	DefaultAccessGroup = "atrium_access"
	DefaultAdminGroup  = "atrium_admin"
)

func init() {
	instance := []settings.Scope{settings.Instance}
	text := func(key string, check func(string) error) settings.Def {
		return settings.Def{Key: key, Module: module, Type: settings.String, Check: check, Class: settings.UI, Scopes: instance}
	}
	settings.MustRegister(
		settings.Def{
			Key: KeyPreset, Module: module, Type: settings.Enum, Default: PresetGeneric,
			Choices: PresetNames(), Class: settings.UI, Scopes: instance,
		},
		text(KeyIssuer, checkIssuer),
		text(KeyClientID, nil),
		text(KeyScopes, nil),
		text(KeyGroupsClaim, nil),
		settings.Def{Key: KeyAccessGroup, Module: module, Type: settings.String, Default: DefaultAccessGroup, Check: settings.NonEmpty, Class: settings.UI, Scopes: instance},
		settings.Def{Key: KeyAdminGroup, Module: module, Type: settings.String, Default: DefaultAdminGroup, Check: settings.NonEmpty, Class: settings.UI, Scopes: instance},
		text(KeyAccessEmails, nil),
		text(KeyAdminEmails, nil),
		settings.Def{Key: KeyIdleDays, Module: module, Type: settings.Int, Default: "7", Check: settings.Between(1, 365), Class: settings.Advanced, Scopes: instance},
		settings.Def{Key: KeyMaxDays, Module: module, Type: settings.Int, Default: "30", Check: settings.Between(1, 365), Class: settings.Advanced, Scopes: instance},
		settings.Def{Key: KeyRecheckMinutes, Module: module, Type: settings.Int, Default: "60", Check: settings.Between(1, 1440), Class: settings.Advanced, Scopes: instance},
	)
}

func checkIssuer(value string) error {
	if py.Strip(value) == "" {
		return nil
	}
	return checkEndpoint(py.Strip(value))
}

func checkEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return settings.Invalid("must be an https URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if loopback(u.Hostname()) {
			return nil
		}
	}
	return settings.Invalid("must be an https URL")
}

func loopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
