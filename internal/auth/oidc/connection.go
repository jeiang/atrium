package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const ConnectionID = "service/oidc"

const (
	cfgPreset        = "preset"
	cfgIssuer        = "issuer"
	cfgClientID      = "client_id"
	cfgAPIClientID   = "api_client_id"
	cfgScopes        = "scopes"
	cfgGroupsClaim   = "groups_claim"
	cfgAccessGroup   = "access_group"
	cfgAdminGroup    = "admin_group"
	cfgAccessEmails  = "access_emails"
	cfgAdminEmails   = "admin_emails"
	cfgAccessDomains = "access_domains"
	cfgAdminDomains  = "admin_domains"
	cfgClientSecret  = "client_secret"
)

var (
	ErrNoCredential  = errors.New("oidc: the connection has no client secret")
	ErrNoConnections = errors.New("oidc: connections are not available")
)

type Connection interface {
	Values() map[string]string
	Credential(slot string) (string, error)
	HTTP(name string) (*http.Client, error)
	Observe(ctx context.Context, result ProbeResult) error
	Close()
}

type Source interface {
	Open(ctx context.Context, d *db.DB, id string) (Connection, error)
}

var source Source

func UseConnections(s Source) { source = s }

func (s *Service) open(ctx context.Context) (Connection, error) {
	if source == nil {
		return nil, ErrNoConnections
	}
	return source.Open(ctx, s.d, ConnectionID)
}

func (s *Service) lease(ctx context.Context) (Connection, error) { return s.open(ctx) }

func (s *Service) recordClientAuth(ctx context.Context, secret string) {
	conn, err := s.open(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	current, err := conn.Credential(cfgClientSecret)
	if err != nil || current != secret {
		return
	}
	_ = conn.Observe(ctx, ProbeResult{
		Capability: "client_auth",
		State:      "ok",
		Reason:     "client authentication proved by an interactive login",
	})
}

func configValues(c Connection) map[string]string { return c.Values() }

type connectionValues struct {
	Preset        string
	Issuer        string
	ClientID      string
	APIClientID   string
	Scopes        string
	GroupsClaim   string
	AccessGroup   string
	AdminGroup    string
	AccessEmails  string
	AdminEmails   string
	AccessDomains string
	AdminDomains  string
}

func ParseConnectionConfig(raw json.RawMessage) (connectionValues, error) {
	var m map[string]string
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return connectionValues{}, fmt.Errorf("oidc config: %w", err)
		}
	}
	var v connectionValues
	for key, value := range m {
		value = strings.TrimSpace(value)
		switch key {
		case cfgPreset:
			v.Preset = value
		case cfgIssuer:
			v.Issuer = value
		case cfgClientID:
			v.ClientID = value
		case cfgAPIClientID:
			v.APIClientID = value
		case cfgScopes:
			v.Scopes = value
		case cfgGroupsClaim:
			v.GroupsClaim = value
		case cfgAccessGroup:
			v.AccessGroup = value
		case cfgAdminGroup:
			v.AdminGroup = value
		case cfgAccessEmails:
			v.AccessEmails = value
		case cfgAdminEmails:
			v.AdminEmails = value
		case cfgAccessDomains:
			v.AccessDomains = value
		case cfgAdminDomains:
			v.AdminDomains = value
		default:
			return connectionValues{}, fmt.Errorf("oidc config has no %s field", key)
		}
	}
	return v, nil
}

func ValidateConnection(raw json.RawMessage) error {
	vals, err := ParseConnectionConfig(raw)
	if err != nil {
		return err
	}
	if vals.Preset != "" {
		if _, ok := presetNamed(vals.Preset); !ok {
			return fmt.Errorf("unknown preset %q", vals.Preset)
		}
	}
	if vals.Issuer != "" {
		if err := checkEndpoint(vals.Issuer); err != nil {
			return fmt.Errorf("issuer %w", err)
		}
	}
	return nil
}

type ProbeResult struct {
	Capability string
	State      string
	Reason     string
	Data       json.RawMessage
}

func Probe(ctx context.Context, conn Connection) []ProbeResult {
	vals, err := ParseConnectionConfig(rawConfig(conn.Values()))
	if err != nil {
		return []ProbeResult{{Capability: "discovery", State: "failing", Reason: err.Error()}}
	}
	issuer := strings.TrimSpace(vals.Issuer)
	if issuer == "" {
		return []ProbeResult{{Capability: "discovery", State: "failing", Reason: "issuer is not set"}}
	}
	if err := checkEndpoint(issuer); err != nil {
		return []ProbeResult{{Capability: "discovery", State: "failing", Reason: "issuer " + err.Error()}}
	}
	hc, err := conn.HTTP(cfgIssuer)
	if err != nil {
		return []ProbeResult{{Capability: "discovery", State: "failing", Reason: err.Error()}}
	}
	hc.CheckRedirect = noRedirect
	p, err := discoverDoc(ctx, hc, issuer, time.Now())
	if err != nil {
		return []ProbeResult{{Capability: "discovery", State: "failing", Reason: err.Error()}}
	}
	if _, err := fetchJWKS(ctx, hc, p.jwksURI); err != nil {
		return []ProbeResult{{Capability: "discovery", State: "failing", Reason: err.Error()}}
	}
	data, err := json.Marshal(map[string]string{
		"issuer":                 p.issuer,
		"authorization_endpoint": p.authURL,
		"token_endpoint":         p.tokenURL,
		"jwks_uri":               p.jwksURI,
	})
	if err != nil {
		return []ProbeResult{{Capability: "discovery", State: "failing", Reason: err.Error()}}
	}
	return []ProbeResult{
		{Capability: "discovery", State: "ok", Reason: "issuer, endpoints and signing keys validated", Data: data},
		{Capability: "client_auth", State: "untested", Reason: "interactive login required"},
	}
}
