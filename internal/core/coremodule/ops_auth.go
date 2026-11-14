package coremodule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/web"
)

type Identity struct {
	authapp.Principal
	APIVersion int `json:"api_version"`
}

func authOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			authapp.DescribeIdentity(), authapp.DescribeConfiguration(), authapp.DescribeSigninStart(), authapp.DescribeSigninComplete(), authapp.DescribeLogout(),
			authapp.DescribeBearerList(), authapp.DescribeBearerIssue(), authapp.DescribeBearerRevoke(),
		}, append(authRoutes(nil), []application.Binding{
			{Kind: application.CLI, Target: "auth token issue", Uses: []application.Name{authapp.BearerIssueName}},
			{Kind: application.CLI, Target: "auth token list", Uses: []application.Name{authapp.BearerListName}},
			{Kind: application.CLI, Target: "auth token revoke", Uses: []application.Name{authapp.BearerRevokeName}},
			{Kind: application.HTML, Target: "GET /settings/auth", Uses: []application.Name{authapp.ConfigurationName, authapp.BearerListName}},
			{Kind: application.HTML, Target: "POST /settings/auth/tokens", Uses: []application.Name{authapp.BearerIssueName, authapp.ConfigurationName, authapp.BearerListName}},
			{Kind: application.HTML, Target: "POST /settings/auth/tokens/{id}/revoke", Uses: []application.Name{authapp.BearerRevokeName, authapp.ConfigurationName, authapp.BearerListName}},
		}...)
}

func authRoutes(d *web.Deps) []application.Binding {
	return []application.Binding{
		api.Query(d, authapp.DescribeIdentity(), application.APIShape{Area: "settings", Response: reflect.TypeFor[Identity](), Errors: []application.Code{application.Unauthenticated}},
			"GET /me", identity),
		api.Query(d, authapp.DescribeConfiguration(), application.APIShape{Area: "settings"},
			"GET /auth", func(ctx context.Context, _ struct{}) (application.Read[authapp.Configuration], error) {
				return d.Account.Configuration(ctx)
			}),
		api.Query(d, authapp.DescribeBearerList(), application.APIShape{Area: "settings", Uncached: true},
			"GET /auth/tokens", func(ctx context.Context, _ struct{}) (application.Read[Tokens], error) {
				read, err := d.Account.BearerList(ctx)
				return application.Read[Tokens]{Revision: read.Revision, Data: Tokens{Tokens: read.Data}}, err
			}),
		api.Command(d, authapp.DescribeBearerIssue(), application.APIShape{Area: "settings", Success: http.StatusCreated, Sensitive: true, Errors: []application.Code{application.Invalid}},
			"POST /auth/tokens", func(ctx context.Context, in authapp.BearerIssueInput) (application.Change[authapp.Issued], error) {
				return d.Account.BearerIssue(ctx, in)
			}),
		api.Command(d, authapp.DescribeBearerRevoke(), application.APIShape{Area: "settings", Body: application.BodyNone, Success: http.StatusNoContent, Errors: []application.Code{application.NotFound}},
			"DELETE /auth/tokens/{token_id}", func(ctx context.Context, in TokenRef) (application.Change[struct{}], error) {
				return d.Account.BearerRevoke(ctx, authapp.BearerRevokeInput{ID: in.ID})
			}),
	}
}

type Tokens struct {
	Tokens []authapp.Token `json:"tokens"`
}

type TokenRef struct {
	ID int64 `path:"token_id" json:"-"`
}

func identity(ctx context.Context, _ struct{}) (application.Read[Identity], error) {
	read, err := authapp.Identity(ctx)
	if err != nil {
		return application.Read[Identity]{}, err
	}
	body := Identity{Principal: read.Data, APIVersion: 1}
	sum := sha256.Sum256([]byte(body.Subject + "\x00" + body.Email + "\x00" + body.Name))
	return application.Read[Identity]{Revision: application.Revision(hex.EncodeToString(sum[:12])), Data: body}, nil
}
