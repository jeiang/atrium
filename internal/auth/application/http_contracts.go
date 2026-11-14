package application

import (
	"context"

	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/auth/oidc"
)

type Sessions = oidc.Service

type Credential = bearer.Token

var (
	ErrInvalid = bearer.ErrInvalid
	ErrRevoked = bearer.ErrRevoked
	ErrExpired = bearer.ErrExpired
	ErrAPIOff  = oidc.ErrAPIOff
	ErrToken   = oidc.ErrToken
	ErrDenied  = oidc.ErrDenied
)

func (s *Service) Authenticate(ctx context.Context, raw string) (Credential, error) {
	return s.bearers.Authenticate(ctx, raw)
}
