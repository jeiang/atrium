package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"golang.org/x/oauth2"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/connections"
)

const (
	OAuthSetupName     application.Name = "connections.oauth-setup"
	OAuthConfigureName application.Name = "connections.oauth-configure"
	OAuthStartName     application.Name = "connections.oauth-start"
	OAuthCompleteName  application.Name = "connections.oauth-complete"
)

type OAuthSetupInput struct{}
type OAuthSetup struct {
	ClientID        string        `json:"client_id"`
	ClientSecretSet bool          `json:"client_secret_set"`
	Capabilities    []GoogleGrant `json:"capabilities"`
	Health          string        `json:"health"`
	Reason          string        `json:"reason"`
}
type OAuthConfigureInput struct {
	ClientID     string
	ClientSecret string `json:"-"`
}
type OAuthStartInput struct {
	Capability string
	Redirect   string
	Loopback   bool
}
type OAuthStartResult struct {
	URL string `json:"-"`
}
type OAuthCompleteInput struct {
	State     string `json:"-"`
	Code      string `json:"-"`
	AuthError string `json:"-"`
	Loopback  bool
}

func DescribeOAuthSetup() application.Definition {
	return application.Describe[OAuthSetupInput, OAuthSetup](OAuthSetupName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeOAuthConfigure() application.Definition {
	return application.Describe[OAuthConfigureInput, View](OAuthConfigureName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeOAuthStart() application.Definition {
	return application.Describe[OAuthStartInput, OAuthStartResult](OAuthStartName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeOAuthComplete() application.Definition {
	return application.Describe[OAuthCompleteInput, GoogleGrant](OAuthCompleteName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func (s *Service) OAuthSetup(ctx context.Context, in OAuthSetupInput) (application.Read[OAuthSetup], error) {
	if err := s.store.EnsureGoogle(ctx); err != nil {
		return application.Read[OAuthSetup]{}, err
	}
	view, err := s.store.Get(ctx, connections.GoogleID)
	if err != nil {
		return application.Read[OAuthSetup]{}, err
	}
	var cfg map[string]string
	if err := json.Unmarshal(view.Config, &cfg); err != nil {
		return application.Read[OAuthSetup]{}, err
	}
	out := OAuthSetup{ClientID: cfg["client_id"], Capabilities: connections.GoogleGrants(), Health: string(view.State), Reason: view.Reason}
	for _, cred := range view.Credentials {
		if cred.Slot == "oauth/client_secret" && cred.Present {
			out.ClientSecretSet = true
		}
	}
	revision, err := s.revision(ctx, OAuthSetupName, in)
	return application.Read[OAuthSetup]{Revision: revision, Data: out}, err
}

func (s *Service) OAuthConfigure(ctx context.Context, in OAuthConfigureInput) (application.Change[View], error) {
	var out View
	auditID, err := s.command(ctx, OAuthConfigureName, []ID{connections.GoogleID}, func(commit connections.Commit) error {
		if err := s.store.EnsureGoogle(ctx); err != nil {
			return err
		}
		cand := connections.Candidate{}
		if in.ClientID != "" {
			cand.Config, _ = json.Marshal(map[string]string{"client_id": in.ClientID})
		}
		if in.ClientSecret != "" {
			cand.Credentials = []connections.CredentialInput{{Slot: "oauth/client_secret", Value: in.ClientSecret}}
		}
		var err error
		out, err = s.store.Save(ctx, connections.GoogleID, cand, commit)
		return err
	})
	return application.Change[View]{Data: out, AuditID: auditID}, err
}

func (s *Service) OAuthStart(ctx context.Context, in OAuthStartInput) (application.Change[OAuthStartResult], error) {
	var out OAuthStartResult
	auditID, err := s.command(ctx, OAuthStartName, []ID{connections.GoogleID}, func(commit connections.Commit) error {
		grant, ok := connections.GoogleGrantFor(in.Capability)
		if !ok {
			return &application.Error{Code: application.Invalid, Message: "unknown capability"}
		}
		conf, err := s.googleOAuth(ctx, in.Redirect, grant.Scope, in.Loopback)
		if err != nil {
			return err
		}
		if conf.ClientID == "" || conf.ClientSecret == "" {
			message := "Save the client ID and client secret first."
			if in.Loopback {
				message = "the google/default connection needs a client ID and client secret"
			}
			if !in.Loopback {
				return &application.Error{Code: application.Invalid, Message: message}
			}
			return consentError(message)
		}
		state, err := googleToken(24)
		if err != nil {
			return err
		}
		verifier, err := googleToken(64)
		if err != nil {
			return err
		}
		out.URL = conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "consent"))
		return s.store.SaveOAuthState(ctx, state, verifier, grant.Name, in.Redirect, commit)
	})
	return application.Change[OAuthStartResult]{Data: out, AuditID: auditID}, err
}

func consentError(message string) error {
	return &application.Error{Code: application.Refused, Message: message}
}

func (s *Service) OAuthComplete(ctx context.Context, in OAuthCompleteInput) (application.Change[GoogleGrant], error) {
	var out GoogleGrant
	auditID, err := s.command(ctx, OAuthCompleteName, []ID{connections.GoogleID}, func(commit connections.Commit) error {
		pending, err := s.store.ConsumeOAuthState(ctx, in.State)
		if err != nil {
			if in.Loopback && errors.Is(err, connections.ErrUnknownConsent) {
				return consentError("state mismatch; start again")
			}
			return err
		}
		refuse := func(message string) error {
			if in.Loopback {
				return consentError(message)
			}
			return &application.Error{Code: application.Invalid, Message: message}
		}
		if in.AuthError != "" {
			return refuse("Google returned an error; start again")
		}
		if time.Since(pending.Created) > connections.GoogleStateTTL {
			return refuse("the consent link expired; start again")
		}
		grant, ok := connections.GoogleGrantFor(pending.Capability)
		if !ok {
			return refuse("the consent link names an unknown capability")
		}
		conf, err := s.googleOAuth(ctx, pending.Redirect, grant.Scope, in.Loopback)
		if err != nil {
			return err
		}
		lease, err := s.store.Open(ctx, connections.GoogleID)
		if err != nil {
			return err
		}
		client, err := lease.Transport()
		lease.Close()
		if err != nil {
			return err
		}
		exchange := context.WithValue(ctx, oauth2.HTTPClient, client)
		token, err := conf.Exchange(exchange, in.Code, oauth2.VerifierOption(pending.Verifier))
		if err != nil {
			return refuse("Google token exchange failed; start again")
		}
		if token.RefreshToken == "" {
			return refuse("Google returned no refresh token; revoke the app's access and retry")
		}
		if _, err := s.store.Save(ctx, connections.GoogleID, connections.Candidate{Credentials: []connections.CredentialInput{{Slot: grant.Slot, Value: token.RefreshToken}}}, commit); err != nil {
			return err
		}
		out = grant
		return nil
	})
	return application.Change[GoogleGrant]{Data: out, AuditID: auditID}, err
}

func (s *Service) googleOAuth(ctx context.Context, redirect, scope string, loopback bool) (*oauth2.Config, error) {
	lease, err := s.store.Open(ctx, connections.GoogleID)
	if err != nil {
		var unknown connections.UnknownID
		if loopback && errors.As(err, &unknown) {
			return nil, consentError("the google/default connection is not configured")
		}
		return nil, err
	}
	defer lease.Close()
	clientID, authURL, tokenURL, _, err := connections.GoogleConfig(lease)
	if err != nil {
		return nil, err
	}
	secret, err := lease.Credential("oauth/client_secret")
	if err != nil {
		return nil, err
	}
	if loopback && s.env != nil {
		if value, ok := s.env("ATRIUM_CALENDAR_GOOGLE_TOKEN_URL"); ok && value != "" {
			tokenURL = value
		}
	}
	return &oauth2.Config{ClientID: clientID, ClientSecret: secret, RedirectURL: redirect, Scopes: []string{scope}, Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams}}, nil
}

func googleToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
