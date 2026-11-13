package application

import (
	"context"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

const (
	ConfigurationName  application.Name = "auth.configuration"
	SigninStartName    application.Name = "auth.signin-start"
	SigninCompleteName application.Name = "auth.signin-complete"
	LogoutName         application.Name = "auth.logout"
)

type Service struct {
	oidc    *oidc.Service
	bearers *bearer.Service
	state   *state.Store
}

type Open func(context.Context) (*Service, func(), error)

func New(d *db.DB, sessions *oidc.Service, bearers *bearer.Service) *Service {
	return &Service{oidc: sessions, bearers: bearers, state: state.New(d)}
}

func CLI(env config.Env) Open {
	return func(ctx context.Context) (*Service, func(), error) {
		d, _, err := config.OpenStore(ctx, env)
		if err != nil {
			return nil, nil, err
		}
		return New(d, nil, bearer.New(d, nil)), func() { _ = d.Close() }, nil
	}
}

type Configuration struct {
	Configured bool     `json:"configured"`
	Active     bool     `json:"active"`
	Provider   string   `json:"provider"`
	Display    string   `json:"display"`
	Issuer     string   `json:"issuer"`
	ClientID   string   `json:"client_id"`
	Redirect   string   `json:"redirect_uri"`
	Origins    []string `json:"origins"`
	Reason     string   `json:"reason"`
}

type SigninStartInput struct {
	Next string `json:"next"`
}

type Signin struct {
	URL     string `json:"url"`
	Binding string `json:"-"`
}

type SigninCompleteInput struct {
	State   string `json:"-"`
	Code    string `json:"-"`
	Binding string `json:"-"`
}

type Session struct {
	Token    string        `json:"-"`
	ReturnTo string        `json:"return_to"`
	MaxAge   time.Duration `json:"max_age"`
}

type LogoutInput struct {
	Token string `json:"-"`
}

type SigninError struct {
	Reason string
	Cause  error
}

func (e *SigninError) Error() string { return e.Reason + ": " + e.Cause.Error() }

func (e *SigninError) Unwrap() error { return e.Cause }

func signinFailure(err error) error {
	var unconfigured *oidc.ConfigError
	reason := ""
	switch {
	case errors.Is(err, oidc.ErrDenied):
		reason = "denied"
	case errors.Is(err, oidc.ErrLoginTimeout):
		reason = "timeout"
	case errors.Is(err, oidc.ErrLogin), errors.Is(err, oidc.ErrNoPKCE), errors.Is(err, oidc.ErrRejected), errors.Is(err, oidc.ErrToken):
		reason = "refused"
	case errors.Is(err, oidc.ErrProvider):
		reason = "unreachable"
	case errors.As(err, &unconfigured):
		reason = "unconfigured"
	default:
		return err
	}
	return &SigninError{Reason: reason, Cause: err}
}

func DescribeConfiguration() application.Definition {
	return application.Describe[struct{}, Configuration](ConfigurationName, application.Query, []application.Authority{application.Operator, application.Bootstrap}, application.NoEffect, false)
}

func DescribeSigninStart() application.Definition {
	return application.Describe[SigninStartInput, Signin](SigninStartName, application.Command, []application.Authority{application.Bootstrap}, application.LocalUndoable, false)
}

func DescribeSigninComplete() application.Definition {
	return application.Describe[SigninCompleteInput, Session](SigninCompleteName, application.Command, []application.Authority{application.Bootstrap}, application.External, false)
}

func DescribeLogout() application.Definition {
	return application.Describe[LogoutInput, struct{}](LogoutName, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, false)
}

func (s *Service) Configuration(ctx context.Context) (application.Read[Configuration], error) {
	rev, err := s.revision(ctx, ConfigurationName)
	if err != nil {
		return application.Read[Configuration]{}, err
	}
	out := Configuration{Configured: s.oidc != nil}
	if s.oidc != nil {
		cfg, err := s.oidc.Config(ctx)
		if err != nil {
			out.Reason = err.Error()
		} else {
			out.Active = true
			out.Display = cfg.Preset.Display
			out.Provider = cfg.Preset.Display
			if out.Provider == "" {
				out.Provider = cfg.Preset.Name
			}
			out.Issuer, out.ClientID, out.Redirect = cfg.Issuer, cfg.ClientID, cfg.RedirectURI
			out.Origins = s.oidc.Origins(cfg)
		}
	}
	return application.Read[Configuration]{Data: out, Revision: rev}, nil
}

func (s *Service) SigninStart(ctx context.Context, in SigninStartInput) (application.Change[Signin], error) {
	login, err := s.oidc.Begin(ctx, in.Next)
	if err != nil {
		return application.Change[Signin]{}, signinFailure(err)
	}
	return application.Change[Signin]{Data: Signin{URL: login.URL, Binding: login.Binding}}, nil
}

func (s *Service) SigninComplete(ctx context.Context, in SigninCompleteInput) (application.Change[Session], error) {
	grant, err := s.oidc.Complete(ctx, in.State, in.Code, in.Binding)
	if err != nil {
		return application.Change[Session]{}, signinFailure(err)
	}
	return application.Change[Session]{Data: Session{Token: grant.Token, ReturnTo: grant.ReturnTo, MaxAge: grant.MaxAge}}, nil
}

func (s *Service) Logout(ctx context.Context, in LogoutInput) (application.Change[struct{}], error) {
	return application.Change[struct{}]{}, s.oidc.End(ctx, in.Token)
}

func (s *Service) revision(ctx context.Context, name application.Name) (application.Revision, error) {
	counters, err := s.state.Read(ctx, nil, []string{state.Auth})
	if err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(name), counters, nil, "")), nil
}
