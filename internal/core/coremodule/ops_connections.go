package coremodule

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/connections"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/connections/connweb"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
)

func connectionsOperations() ([]application.Definition, []application.Binding) {
	defs, bindings := connectionsDeclared()
	return defs, append(connectionRoutes(nil), bindings...)
}

func connectionsDeclared() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			connectionsapp.DescribeList(), connectionsapp.DescribeOAuthSetup(), connectionsapp.DescribeTestSave(), connectionsapp.DescribeSaveFields(), connectionsapp.DescribeDisconnect(), connectionsapp.DescribeOAuthConfigure(), connectionsapp.DescribeOAuthStart(), connectionsapp.DescribeOAuthComplete(), connectionsapp.DescribeTest(), connectionsapp.DescribeObserveHealth(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "connections list", Uses: []application.Name{connectionsapp.ListName}},
			{Kind: application.CLI, Target: "connections test", Uses: []application.Name{connectionsapp.TestName}},
			{Kind: application.HTML, Target: "GET /settings/connections", Uses: []application.Name{connectionsapp.ListName}},
			{Kind: application.HTML, Target: "POST /settings/connections/test", Uses: []application.Name{connectionsapp.TestSaveName, connectionsapp.ListName}},
			{Kind: application.HTML, Target: "POST /settings/connections/disconnect", Uses: []application.Name{connectionsapp.DisconnectName}},
			{Kind: application.HTML, Target: "GET /settings/connections/google", Uses: []application.Name{connectionsapp.OAuthSetupName}},
			{Kind: application.HTML, Target: "POST /settings/connections/google", Uses: []application.Name{connectionsapp.OAuthConfigureName, connectionsapp.OAuthSetupName}},
			{Kind: application.HTML, Target: "POST /settings/connections/google/start", Uses: []application.Name{connectionsapp.OAuthStartName, connectionsapp.OAuthSetupName}},
			{Kind: application.HTML, Target: "GET /settings/connections/google/callback", Uses: []application.Name{connectionsapp.OAuthCompleteName, connectionsapp.OAuthSetupName}},
			{Kind: application.HTML, Target: "POST /settings/connections/google/paste", Uses: []application.Name{connectionsapp.OAuthCompleteName, connectionsapp.OAuthSetupName}},
			{Kind: application.HTML, Target: "POST /settings/connections/google/test", Uses: []application.Name{connectionsapp.TestName, connectionsapp.OAuthSetupName}},
			{Kind: application.Service, Target: "connection-health", Uses: []application.Name{connectionsapp.ObserveHealthName}},
		}
}

func connectionService(d *web.Deps) (*connectionsapp.Service, error) {
	return connectionsapp.New(d.Ctx, d.DB)
}

func watchConnections(ctx context.Context, d *db.DB) error {
	ctx = audit.With(ctx, "service:connection-health", audit.Job)
	svc, err := connectionsapp.New(ctx, d)
	if err != nil {
		return err
	}
	observe := func(in connectionsapp.ObserveHealthInput) {
		if _, err := svc.ObserveHealth(ctx, in); err != nil && ctx.Err() == nil {
			log.Warn("connection health check failed", logx.Err, err.Error())
		}
	}
	observe(connectionsapp.ObserveHealthInput{Stale: true})
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			observe(connectionsapp.ObserveHealthInput{})
		case failed := <-connections.Failed():
			observe(connectionsapp.ObserveHealthInput{ID: failed.ID})
		}
	}
}

type ConnectionRef struct {
	Kind string `path:"kind" json:"-"`
	Name string `path:"name" json:"-"`
}

func (c ConnectionRef) id() connectionsapp.ID { return connectionsapp.ID(c.Kind + "/" + c.Name) }

type ConnectionTestSave struct {
	Kind        string            `path:"kind" json:"-"`
	Name        string            `path:"name" json:"-"`
	Label       string            `json:"label,omitempty"`
	URL         string            `json:"url,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty" api:"writeonly"`
}

type Connections struct {
	Connections []connectionsapp.View `json:"connections"`
}

type GoogleClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret" api:"writeonly"`
}

type GoogleStart struct {
	Capability string `json:"capability"`
}

type GoogleConsent struct {
	URL   string `json:"authorize_url"`
	State string `json:"state"`
}

type GoogleComplete struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

func connectionRoutes(d *web.Deps) []application.Binding {
	const area = "integrations"
	svc := func() (*connectionsapp.Service, error) { return connectionService(d) }
	refusable := []application.Code{application.Invalid, application.NotFound, application.Refused, application.Conflict}
	return []application.Binding{
		api.Query(d, connectionsapp.DescribeList(), application.APIShape{Area: area},
			"GET /connections", func(ctx context.Context, _ struct{}) (application.Read[Connections], error) {
				s, err := svc()
				if err != nil {
					return application.Read[Connections]{}, err
				}
				read, err := s.List(ctx, connectionsapp.ListInput{})
				return application.Read[Connections]{Revision: read.Revision, Data: Connections{Connections: read.Data}}, err
			}),
		api.Command(d, connectionsapp.DescribeTest(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable},
			"POST /connections/{kind}/{name}/test", func(ctx context.Context, in ConnectionRef) (application.Change[Connections], error) {
				s, err := svc()
				if err != nil {
					return application.Change[Connections]{}, err
				}
				change, err := s.Test(ctx, connectionsapp.TestInput{ID: in.id()})
				return application.Change[Connections]{Data: Connections{Connections: change.Data}, AuditID: change.AuditID}, err
			}),
		api.Command(d, connectionsapp.DescribeTestSave(), application.APIShape{Area: area, Errors: refusable},
			"PUT /connections/{kind}/{name}", func(ctx context.Context, in ConnectionTestSave) (application.Change[connectionsapp.View], error) {
				s, err := svc()
				if err != nil {
					return application.Change[connectionsapp.View]{}, err
				}
				return s.TestSave(ctx, connectionsapp.TestSaveInput{ID: connectionsapp.ID(in.Kind + "/" + in.Name), Label: in.Label, URL: in.URL, Credentials: in.Credentials})
			}),
		api.Command(d, connectionsapp.DescribeDisconnect(), application.APIShape{Area: area, Body: application.BodyNone, Success: http.StatusNoContent, Errors: refusable},
			"DELETE /connections/{kind}/{name}", func(ctx context.Context, in ConnectionRef) (application.Change[struct{}], error) {
				s, err := svc()
				if err != nil {
					return application.Change[struct{}]{}, err
				}
				return s.Disconnect(ctx, connectionsapp.DisconnectInput{ID: in.id()})
			}),
		api.Query(d, connectionsapp.DescribeOAuthSetup(), application.APIShape{Area: area},
			"GET /connections/google/oauth", func(ctx context.Context, _ struct{}) (application.Read[connectionsapp.OAuthSetup], error) {
				s, err := svc()
				if err != nil {
					return application.Read[connectionsapp.OAuthSetup]{}, err
				}
				return s.OAuthSetup(ctx, connectionsapp.OAuthSetupInput{})
			}),
		api.Command(d, connectionsapp.DescribeOAuthConfigure(), application.APIShape{Area: area, Errors: refusable},
			"PUT /connections/google/oauth", func(ctx context.Context, in GoogleClient) (application.Change[connectionsapp.View], error) {
				s, err := svc()
				if err != nil {
					return application.Change[connectionsapp.View]{}, err
				}
				return s.OAuthConfigure(ctx, connectionsapp.OAuthConfigureInput{ClientID: in.ClientID, ClientSecret: in.ClientSecret})
			}),
		api.Command(d, connectionsapp.DescribeOAuthStart(), application.APIShape{Area: area, Sensitive: true, Errors: refusable},
			"POST /connections/google/oauth/start", func(ctx context.Context, in GoogleStart) (application.Change[GoogleConsent], error) {
				s, err := svc()
				if err != nil {
					return application.Change[GoogleConsent]{}, err
				}
				change, err := s.OAuthStart(ctx, connectionsapp.OAuthStartInput{Capability: in.Capability, Redirect: connweb.GoogleRedirect(web.RequestOrigin(ctx))})
				if err != nil {
					return application.Change[GoogleConsent]{}, err
				}
				consent := GoogleConsent{URL: change.Data.URL}
				if parsed, perr := url.Parse(change.Data.URL); perr == nil {
					consent.State = parsed.Query().Get("state")
				}
				return application.Change[GoogleConsent]{Data: consent, AuditID: change.AuditID}, nil
			}),
		api.Command(d, connectionsapp.DescribeOAuthComplete(), application.APIShape{Area: area, Errors: refusable},
			"POST /connections/google/oauth/complete", func(ctx context.Context, in GoogleComplete) (application.Change[connectionsapp.GoogleGrant], error) {
				s, err := svc()
				if err != nil {
					return application.Change[connectionsapp.GoogleGrant]{}, err
				}
				return s.OAuthComplete(ctx, connectionsapp.OAuthCompleteInput{State: in.State, Code: in.Code})
			}),
	}
}
