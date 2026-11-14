package web

import (
	"github.com/jeiang/atrium/internal/application"
	authapp "github.com/jeiang/atrium/internal/auth/application"
)

func BuiltinBindings(oidc bool) []application.Binding {
	out := []application.Binding{
		{Kind: application.ProtocolOnly, Target: "GET /", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "GET /healthz", Reason: "transport"},
		{Kind: application.ProtocolOnly, Target: "* /static/*", Reason: "static"},
	}
	if oidc {
		out = append(out,
			application.Binding{Kind: application.HTML, Target: "GET /login", Uses: []application.Name{authapp.ConfigurationName}},
			application.Binding{Kind: application.HTML, Target: "POST /login", Uses: []application.Name{authapp.SigninStartName}},
			application.Binding{Kind: application.HTML, Target: "GET /auth/callback", Uses: []application.Name{authapp.SigninCompleteName}},
			application.Binding{Kind: application.HTML, Target: "POST /logout", Uses: []application.Name{authapp.LogoutName}},
		)
	}
	return out
}
