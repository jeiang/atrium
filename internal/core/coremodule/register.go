package coremodule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/auth/bearercmd"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidcconn"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/connections/connectionscmd"
	"github.com/jeiang/atrium/internal/connections/connweb"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbcmd"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/ledger/ledgercmd"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/obs/opsmetrics"
	"github.com/jeiang/atrium/internal/observability"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/ops/collectors"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/proposalscmd"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/secrets/secretscmd"
	"github.com/jeiang/atrium/internal/settings"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
	"github.com/jeiang/atrium/internal/usagepages"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/writes/writescmd"
)

type Module struct {
	Version   string
	Guard     settingsapp.Guard
	Clients   func(config.Config) mail.OpenFunc
	Now       func() float64
	Resources module.Resources
	Jobs      jobworker.Dependencies
}

func (m Module) namer(env config.Env) func(context.Context, *db.DB, config.Config) (rebalance.Namer, error) {
	return func(ctx context.Context, d *db.DB, cfg config.Config) (rebalance.Namer, error) {
		if m.Jobs.Classifier == nil {
			return nil, errors.New("proposal scans are not bound to a classifier")
		}
		svc := service.New(ctx, d, cfg, env, nil)
		svc.Now = m.Now
		c, err := m.Jobs.Classifier(ctx, svc)
		if err != nil {
			return nil, err
		}
		return rebalance.SuggestName(c), nil
	}
}

func (m Module) Declare(mc module.Context) module.Declaration {
	operations, bindings := authOperations()
	reviewOps, reviewBindings := reviewOperations()
	operations, bindings = append(operations, reviewOps...), append(bindings, reviewBindings...)
	settingOps, settingBindings := settingsOperations()
	operations, bindings = append(operations, settingOps...), append(bindings, settingBindings...)
	connectionOps, connectionBindings := connectionsOperations()
	operations, bindings = append(operations, connectionOps...), append(bindings, connectionBindings...)
	maintenanceOps, maintenanceBindings := maintenanceOperations()
	operations, bindings = append(operations, maintenanceOps...), append(bindings, maintenanceBindings...)
	maintenance := maintenanceapp.New(mc.Env, m.Now)
	openSettings := settingsapp.CLI(mc.Env, m.Guard)
	enabled := func(ctx context.Context, q db.Querier, name string) (bool, error) {
		snap, err := mc.Evaluate(ctx, q)
		if err != nil {
			return false, err
		}
		return snap.Enabled(name), nil
	}
	store := func(d *db.DB, cfg config.Config) *ledger.Store {
		s, err := mc.Catalog().Ledger(d, mc.Env, m.Clients(cfg), m.Now)
		if err != nil {
			panic(err)
		}
		return s
	}
	return module.Declaration{
		Name:              module.Core,
		Core:              true,
		Settings:          slices.Concat(config.Settings(), dbkey.Settings(), oidc.Settings(), secrets.Settings()),
		Host:              slices.Concat(config.HostSettings(), secrets.HostSettings(), observability.HostSettings(), ops.HostSettings()),
		Attention:         AttentionSources(),
		Metrics:           slices.Concat(collectors.Metrics(), opsmetrics.Metrics(), passrun.Metrics(), scheduler.Metrics()),
		Checks:            slices.Concat(ops.Checks(), passrun.Checks()),
		Connections:       slices.Concat(connections.Connections(), oidcconn.Connections(), endpoint.Connections()),
		AccountCredential: connections.AccountCredential(),
		Pages: []web.Page{
			{Module: about.Module(m.Version), Templates: about.Templates(), Nav: []web.NavItem{
				{Label: "About", URL: "/about", Icon: "file-text", Page: "about", Footer: true, Order: 10},
			}},
			{Module: usagepages.Module(), Templates: usagepages.Templates(), Nav: []web.NavItem{
				{Group: "Instance", Label: "Usage", URL: usagepages.URL, Page: "usage", Settings: true, Order: 60},
			}},
			{Module: reviewpages.Module(reviewpages.Options{Clients: m.Clients, Tools: mc.Tools, Enabled: enabled}), Templates: reviewpages.Templates(), Public: actionlink.PublicCheck, Nav: []web.NavItem{
				{Section: "review", Label: "Needs you", URL: "/review", Icon: "list-checks", Page: "review_needs", Order: 10},
				{Section: "review", Label: "Done", URL: "/activity", Icon: "history", Page: "activity", Order: 60},
			}},
			{Module: web.CoreModule, Nav: []web.NavItem{
				{Label: "Sign-in", URL: "/settings/auth", Page: "auth", Settings: true, Primary: true, Order: 50},
			}, OIDC: oidcconn.Source()},
			connweb.Page(connectionService),
		},
		Groups:     SettingGroups(),
		Operations: operations,
		Bindings:   bindings,
		Services:   m.services(mc),
		Recover:    recoverProposals,
		Commands: []*cli.Command{
			writescmd.Command(writescmd.Options{Env: mc.Env, Now: m.Now, Store: store}),
			ledgercmd.Command(ledgercmd.Options{Env: mc.Env, Now: m.Now, Store: store}),
			proposalscmd.Command(proposalscmd.Options{Env: mc.Env, Clients: m.Clients, Now: m.Now, Enabled: enabled, Tools: mc.Tools, Store: store, Namer: m.namer(mc.Env)}),
			settingscmd.Command(openSettings),
			connectionscmd.Command(connectionsapp.CLI(mc.Env)),
			bearercmd.Command(authapp.CLI(mc.Env)),
			secretscmd.Command(openSettings),
			dbcmd.Command(maintenance),
			dbcmd.BackupCommand(maintenance),
			dbcmd.RestoreCommand(maintenance),
			catalogCommand(mc),
		},
	}
}

func AttentionSources() []attention.Source {
	return []attention.Source{attentionsources.Proposals(), attentionsources.ClassificationBudget()}
}

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "sessions", Title: "Sessions", Page: "/settings/auth", Keys: []string{oidc.KeyIdleDays, oidc.KeyMaxDays}},
		{Name: "budget", Title: "Monthly budget", Page: usagepages.URL, Keys: []string{inference.BudgetKey, inference.ChatKey, inference.UnattendedKey}},
	}
}

var log = logx.Logger("atrium.core")

const (
	embedEvery       = time.Minute
	embedStepTimeout = 20 * time.Minute
	recoverInterval  = time.Minute
	recoveryStall    = 5 * time.Minute
)

func recoverProposals(ctx context.Context, d *db.DB, _ config.Config) error {
	n, err := proposals.RecoverStuck(ctx, d, float64(time.Now().UnixNano())/1e9)
	if n > 0 {
		log.Warn("recovered interrupted approved proposals", "count", n)
	}
	return err
}

func (m Module) services(mc module.Context) []runtime.ServiceDefinition {
	return []runtime.ServiceDefinition{
		{Name: "recovery", Critical: true, Stall: recoveryStall, Run: func(ctx context.Context, s *runtime.Services) error {
			d, cfg, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return recoverEvery(ctx, s, d, cfg, mc.Current)
		}},
		{Name: "embedding-index", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return embedIndex(ctx, d)
		}},
		{Name: "connection-health", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return watchConnections(ctx, d)
		}},
	}
}

func recoverEvery(ctx context.Context, s *runtime.Services, d *db.DB, cfg config.Config, current func() *module.Snapshot) error {
	t := time.NewTicker(recoverInterval)
	defer t.Stop()
	s.Heartbeat("recovery")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if err := db.Ready(ctx, d); err != nil && ctx.Err() == nil {
			log.Error("database refresh failed", logx.Err, err.Error())
		}
		if snap := current(); snap != nil {
			if err := snap.Recover(ctx, d, cfg); err != nil && ctx.Err() == nil {
				log.Error("recovery failed", logx.Err, err.Error())
			}
		}
		s.Heartbeat("recovery")
	}
}

func kbPending(ctx context.Context, d *db.DB) error {
	embedURL, err := embed.EmbedURL(ctx, d.Read)
	if err != nil {
		return nil
	}
	modelCtx, err := embed.WithModels(ctx, d.Read)
	if err != nil {
		return nil
	}
	_, err = embed.KBPending(modelCtx, d, embedURL, nil)
	var unavailable *httpx.ServiceUnavailable
	if errors.As(err, &unavailable) || errors.Is(err, httpx.ErrPayload) || errors.Is(err, httpx.ErrMissing) {
		return nil
	}
	return err
}

func embedIndex(ctx context.Context, d *db.DB) error {
	for {
		more, err := embedStep(ctx, d)
		if err != nil {
			return err
		}
		if more {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(embedEvery):
		}
	}
}

func embedStep(ctx context.Context, d *db.DB) (bool, error) {
	step, cancel := context.WithTimeout(ctx, embedStepTimeout)
	defer cancel()
	more, err := mail.ReembedStep(step, d)
	if err != nil && ctx.Err() == nil {
		return false, fmt.Errorf("embedding index rebuild failed: %w", err)
	}
	if more || ctx.Err() != nil {
		return more, ctx.Err()
	}
	if err := kbPending(step, d); err != nil && ctx.Err() == nil {
		return false, fmt.Errorf("knowledge embedding failed: %w", err)
	}
	return false, ctx.Err()
}
