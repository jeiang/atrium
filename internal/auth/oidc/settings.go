package oidc

import (
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/settings"
)

const (
	module = "auth"

	KeyPreset        = "oidc_preset"
	KeyIssuer        = "oidc_issuer"
	KeyClientID      = "oidc_client_id"
	KeyAPIClientID   = "oidc_api_client_id"
	KeyScopes        = "oidc_scopes"
	KeyGroupsClaim   = "oidc_groups_claim"
	KeyAccessGroups  = "oidc_access_groups"
	KeyAccessEmails  = "oidc_access_emails"
	KeyAccessDomains = "oidc_access_domains"
	KeyIdleDays      = "oidc_session_idle_days"
	KeyMaxDays       = "oidc_session_max_days"

	DefaultAccessGroups = "atrium_access,atrium_admin"
)

func init() {
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{Key: KeyIdleDays, Label: "Sign out after idle (days)", Module: module, Type: settings.Int, Default: "7", Check: settings.Between(1, 365)}),
		settings.Value(settings.UI, settings.Def{Key: KeyMaxDays, Label: "Sign out after (days)", Module: module, Type: settings.Int, Default: "30", Check: settings.Between(1, 365)}),
	)
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
