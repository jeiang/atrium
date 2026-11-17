package coremodule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/scheduler/jobcmd"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/commands"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/auth/bearercmd"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidcconn"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/connections/connectionscmd"
	"github.com/jeiang/atrium/internal/davmirror"
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
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/obs/opsmetrics"
	"github.com/jeiang/atrium/internal/observability"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/ops/collectors"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/proposals"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
	"github.com/jeiang/atrium/internal/proposals/proposalscmd"
	"github.com/jeiang/atrium/internal/review/hub"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/secrets/secretscmd"
	"github.com/jeiang/atrium/internal/settings"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/webusage"
)

type Module struct {
	Version   string
	Guard     settingsapp.Guard
	Now       func() float64
	Resources module.Resources
	Jobs      jobworker.Dependencies
}

func (m Module) Declare(mc module.Context) module.Declaration {
	operations, bindings := authOperations()
	reviewOps, reviewBindings := reviewOperations()
	operations, bindings = append(operations, reviewOps...), append(bindings, reviewBindings...)
	settingOps, settingBindings := m.settingsOperations()
	operations, bindings = append(operations, settingOps...), append(bindings, settingBindings...)
	connectionOps, connectionBindings := connectionsOperations()
	operations, bindings = append(operations, connectionOps...), append(bindings, connectionBindings...)
	maintenanceOps, maintenanceBindings := m.maintenanceOperations()
	operations, bindings = append(operations, maintenanceOps...), append(bindings, maintenanceBindings...)
	readModelOps, readModelBindings := m.readModelOperations()
	operations, bindings = append(operations, readModelOps...), append(bindings, readModelBindings...)
	commitmentOps, commitmentBindings := commitmentOperations()
	operations, bindings = append(operations, commitmentOps...), append(bindings, commitmentBindings...)
	operations = append(operations, webusage.DescribeRoutes())
	bindings = append(bindings, application.Binding{Kind: application.CLI, Target: "usage routes", Uses: []application.Name{webusage.RoutesName}})
	bindings = append(bindings, application.Binding{Kind: application.CLI, Target: proposalsapp.ScanCommand, Composite: true})
	maintenance := maintenanceapp.New(mc.Env)
	openSettings := settingsapp.CLI(mc.Env, m.Guard)
	commandOps, commandBindings := commandOperations()
	operations, bindings = append(operations, commandOps...), append(bindings, commandBindings...)
	eventOps, eventBindings := eventOperations()
	operations, bindings = append(operations, eventOps...), append(bindings, eventBindings...)
	searchOps, searchBindings := searchOperations(m.Now)
	operations, bindings = append(operations, searchOps...), append(bindings, searchBindings...)
	pauseOps, pauseBindings := pauseOperations()
	operations, bindings = append(operations, pauseOps...), append(bindings, pauseBindings...)
	appsOps, appsBindings := m.appsOperations()
	operations, bindings = append(operations, appsOps...), append(bindings, appsBindings...)
	bindings = append(bindings, finalBindings()...)
	enabled := func(ctx context.Context, q db.Querier, name string) (bool, error) {
		snap, err := mc.Evaluate(ctx, q)
		if err != nil {
			return false, err
		}
		return snap.Enabled(name), nil
	}
	store := func(d *db.DB, cfg config.Config) *ledger.Store {
		s, err := mc.Catalog().Ledger(d, mc.Env, cfg, m.Now)
		if err != nil {
			panic(err)
		}
		return s
	}
	openLedger := func(ctx context.Context) (*reviewapp.Service, func(), error) {
		cfg, err := config.Load(mc.Env)
		if err != nil {
			return nil, nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return nil, nil, err
		}
		return reviewapp.New(reviewapp.Deps{DB: d, Ledger: store(d, cfg)}), func() { _ = d.Close() }, nil
	}
	return module.Declaration{
		Name:     module.Core,
		Title:    "Core",
		Core:     true,
		Settings: slices.Concat(config.Settings(), dbkey.Settings(), oidc.Settings(), secrets.Settings()),
		Host:     slices.Concat(config.HostSettings(), backup.HostSettings(), secrets.HostSettings(), observability.HostSettings(), ops.HostSettings()),
		Jobs: []scheduler.Definition{backup.Job(mc.Env, func(ctx context.Context, dest string, forget func(context.Context, db.Querier) error) error {
			_, err := maintenance.Backup(ctx, maintenanceapp.BackupInput{Dest: dest, Forget: forget})
			return err
		})},
		Attention: AttentionSources(),
		Metrics:   slices.Concat(collectors.Metrics(), opsmetrics.Metrics(), passrun.Metrics(), scheduler.Metrics(), []ops.Metric{webusage.Metric()}),
		Queues:    collectors.Queues(),
		Checks: slices.Concat(ops.Checks(), passrun.Checks(), []ops.Check{{Name: "connection owners", Run: func(ctx context.Context, d *db.DB) error {
			return connections.CheckOwners(ctx, d.Read, config.From(ctx).Host())
		}}}),
		Connections:       slices.Concat(connections.Connections(), davmirror.Connections(), oidcconn.Connections(), endpoint.Connections()),
		AccountCredential: connections.AccountCredential(),
		Pages: []web.Page{
			{Module: about.Module, Nav: []web.NavItem{
				{Label: "About", URL: "/about", Icon: "file-text", Page: "about", Footer: true, Order: 10},
				{Group: "Instance", Label: "Usage", URL: usageURL, Page: "usage", Settings: true, Order: 60},
				{Label: "Connections", URL: connections.Screen, Page: "connections", Settings: true, Order: 30},
			}},
			{Module: func(_ chi.Router, d *web.Deps) {
				svc := hub.Compose(hub.ComposeOptions{Tools: mc.Tools, Enabled: enabled})(d).Review
				reviewRoutes(d, func() *reviewapp.Service { return svc })
				d.OnShell(func(ctx context.Context, s *web.ShellState) error {
					if module.SetFrom(ctx) == nil {
						ctx = module.WithSet(ctx, module.SetFrom(d.Ctx))
					}
					n, err := svc.NeedsCount(ctx)
					s.NeedsYou = n
					return err
				})
			}, Nav: []web.NavItem{
				{Section: "review", Label: "Needs you", URL: "/review", Icon: "list-checks", Page: "review_needs", Order: 10},
				{Section: "review", Label: "Done", URL: "/activity", Icon: "history", Page: "activity", Order: 60},
			}},
			{Nav: []web.NavItem{
				{Hub: true, Label: "Review", Icon: "inbox", Page: "review", Badge: true, Order: 10},
				{Hub: true, Label: "Library", Icon: "library", Page: "library", Order: 20},
				{Hub: true, Label: "Apps", Icon: "layout-grid", Page: "apps", Order: 30},
				{Label: "Sign-in", URL: "/settings/auth", Page: "auth", Settings: true, Primary: true, Order: 50},
			}, OIDC: oidcconn.Source()},
			{Module: func(_ chi.Router, d *web.Deps) {
				authRoutes(d)
				commandRoutes(d)
				eventRoutes(d)
				pauseRoutes(d)
				m.appsRoutes(d)
				m.settingsRoutes(d)
				connectionRoutes(d)
				m.maintenanceRoutes(d)
				m.readModelRoutes(d)
				searchRoutes(d, m.Now)
				commitmentRoutes(d, m.commitmentService(d))
			}},
		},
		Groups:     SettingGroups(),
		Operations: operations,
		Bindings:   bindings,
		Services:   m.services(mc),
		Recover:    recoverProposals,
		Commands: []*cli.Command{
			ledgercmd.Command(ledgercmd.Options{Now: m.Now, Open: openLedger}),
			proposalscmd.Command(proposalscmd.Options{Now: m.Now, Session: proposalsDeps{Env: mc.Env, Now: m.Now, Enabled: enabled, Tools: mc.Tools, Store: store}.session}),
			settingscmd.Command(openSettings),
			connectionscmd.Command(connectionsapp.CLI(mc.Env)),
			bearercmd.Command(authapp.CLI(mc.Env)),
			webusage.Command(mc.Env),
			secretscmd.Command(openSettings),
			dbcmd.Command(maintenance),
			dbcmd.BackupCommand(maintenance),
			dbcmd.RestoreCommand(maintenance),
			catalogCommand(mc),
			jobcmd.Command(),
		},
	}
}

func AttentionSources() []attention.Source {
	return []attention.Source{attentionsources.Proposals(), attentionsources.ClassificationBudget()}
}

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "sessions", Title: "Sessions", Page: "/settings/auth", Keys: []string{oidc.KeyIdleDays, oidc.KeyMaxDays}},
		{Name: "budget", Title: "Monthly budget", Page: usageURL, Keys: []string{inference.BudgetKey, inference.ChatKey, inference.UnattendedKey}},
		{Name: "general", Title: "General", Keys: []string{config.KeyTZ}},
	}
}

var log = logx.Logger("atrium.core")

const (
	embedEvery       = time.Minute
	embedStepTimeout = 20 * time.Minute
	recoverInterval  = time.Minute
	recoveryStall    = 5 * time.Minute
	usageURL         = "/settings/usage"
)

func recoverProposals(ctx context.Context, d *db.DB, _ config.Config) error {
	n, err := proposals.RecoverStuck(ctx, d, float64(time.Now().UnixNano())/1e9)
	if n > 0 {
		log.Warn("recovered interrupted approved proposals", "count", n)
	}
	if err != nil {
		return err
	}
	return commands.New(d).Prune(ctx)
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
			return embedIndex(ctx, d, mc.Catalog().Corpora())
		}},
		{Name: "connection-health", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return watchConnections(ctx, d)
		}},
		{Name: "module-flags", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return followFlags(ctx, d, mc.Follow)
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

func drainBackground(ctx context.Context, d *db.DB, corpora []embed.Corpus) error {
	embedURL, err := embed.EmbedURL(ctx, d.Read)
	if err != nil {
		return nil
	}
	modelCtx, err := embed.WithModels(ctx, d.Read)
	if err != nil {
		return nil
	}
	for _, c := range corpora {
		if !c.Background {
			continue
		}
		_, err = embed.Drain(modelCtx, d, embedURL, c, nil)
		var unavailable *httpx.ServiceUnavailable
		if errors.As(err, &unavailable) || errors.Is(err, httpx.ErrPayload) || errors.Is(err, httpx.ErrMissing) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func embedIndex(ctx context.Context, d *db.DB, corpora []embed.Corpus) error {
	ctx = runtime.Unattended(ctx)
	for {
		more, err := embedStep(ctx, d, corpora)
		if until, held := connections.HeldUntil(err); held {
			log.Info("embedding index paused after a refusal", "until", until.UTC().Format(time.RFC3339))
			return runtime.Hold(err, time.Until(until))
		}
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

func embedStep(ctx context.Context, d *db.DB, corpora []embed.Corpus) (bool, error) {
	step, cancel := context.WithTimeout(ctx, embedStepTimeout)
	defer cancel()
	more, err := embed.Reembed(step, d, corpora)
	if err != nil && ctx.Err() == nil {
		return false, fmt.Errorf("embedding index rebuild failed: %w", err)
	}
	if more || ctx.Err() != nil {
		return more, ctx.Err()
	}
	if err := drainBackground(step, d, corpora); err != nil && ctx.Err() == nil {
		return false, fmt.Errorf("knowledge embedding failed: %w", err)
	}
	return false, ctx.Err()
}
