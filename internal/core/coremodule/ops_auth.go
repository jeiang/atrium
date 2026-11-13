package coremodule

import (
	"github.com/jeiang/atrium/internal/application"
	authapp "github.com/jeiang/atrium/internal/auth/application"
)

func authOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{authapp.DescribeIdentity()}, []application.Binding{
		{Kind: application.API, Target: "GET /me", Uses: []application.Name{authapp.IdentityName}},
	}
}
