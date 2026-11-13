package application

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/application"
)

const IdentityName application.Name = "auth.identity"

type Principal struct {
	Source  string     `json:"-"`
	Subject string     `json:"subject"`
	Email   string     `json:"email"`
	Name    string     `json:"name"`
	Expires *time.Time `json:"-"`
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalOf(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func DescribeIdentity() application.Definition {
	return application.Describe[struct{}, Principal](IdentityName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func Identity(ctx context.Context) (application.Read[Principal], error) {
	p, ok := PrincipalOf(ctx)
	if !ok {
		return application.Read[Principal]{}, &application.Error{Code: application.Unauthenticated, Message: "Sign in first."}
	}
	return application.Read[Principal]{Data: p}, nil
}
