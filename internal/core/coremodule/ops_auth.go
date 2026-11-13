package coremodule

import (
	"github.com/jeiang/atrium/internal/application"
	authapp "github.com/jeiang/atrium/internal/auth/application"
)

func authOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			authapp.DescribeIdentity(), authapp.DescribeConfiguration(), authapp.DescribeSigninStart(), authapp.DescribeSigninComplete(), authapp.DescribeLogout(),
			authapp.DescribeBearerList(), authapp.DescribeBearerIssue(), authapp.DescribeBearerRevoke(),
		}, []application.Binding{
			{Kind: application.API, Target: "GET /me", Uses: []application.Name{authapp.IdentityName}},
			{Kind: application.CLI, Target: "auth token issue", Uses: []application.Name{authapp.BearerIssueName}},
			{Kind: application.CLI, Target: "auth token list", Uses: []application.Name{authapp.BearerListName}},
			{Kind: application.CLI, Target: "auth token revoke", Uses: []application.Name{authapp.BearerRevokeName}},
			{Kind: application.HTML, Target: "GET /settings/auth", Uses: []application.Name{authapp.ConfigurationName, authapp.BearerListName}},
			{Kind: application.HTML, Target: "POST /settings/auth/tokens", Uses: []application.Name{authapp.BearerIssueName, authapp.ConfigurationName, authapp.BearerListName}},
			{Kind: application.HTML, Target: "POST /settings/auth/tokens/{id}/revoke", Uses: []application.Name{authapp.BearerRevokeName, authapp.ConfigurationName, authapp.BearerListName}},
		}
}
