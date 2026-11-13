package coremodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/connections"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
)

func connectionsOperations() ([]application.Definition, []application.Binding) {
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
