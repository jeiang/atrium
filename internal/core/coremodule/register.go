package coremodule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/actionlink"
	actionlinkpages "github.com/jeiang/atrium/internal/actionlink/pages"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidcconn"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/connections/connweb"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbcmd"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/ledger/ledgercmd"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
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
	"github.com/jeiang/atrium/internal/settings/settingscmd"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/writes/writescmd"
)

type Module struct {
	Version   string
	Guard     settingscmd.Guard
	Clients   func(config.Config) mail.OpenFunc
	Now       func() float64
	Resources module.Resources
}

func (m Module) Declare(mc module.Context) module.Declaration {
	operations, bindings := authOperations()
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
			{Module: reviewpages.Module(reviewpages.Options{Clients: m.Clients, Tools: mc.Tools, Enabled: enabled}), Templates: reviewpages.Templates(), Public: actionlink.PublicCheck, Nav: []web.NavItem{
				{Section: "review", Label: "Needs you", URL: "/review", Icon: "list-checks", Page: "review_needs", Order: 10},
				{Section: "review", Label: "Done", URL: "/activity", Icon: "history", Page: "activity", Order: 60},
			}},
			{Module: actionlinkpages.Module, Templates: actionlinkpages.Templates()},
			{Module: web.CoreModule, Nav: []web.NavItem{
				{Label: "Sign-in", URL: "/settings/auth", Page: "auth", Settings: true, Primary: true, Order: 50},
			}, OIDC: oidcconn.Source()},
			connweb.Page(),
		},
		Groups:     SettingGroups(),
		Operations: operations,
		Bindings:   bindings,
		Services:   m.services(mc),
		Recover:    recoverProposals,
		Commands: []*cli.Command{
			writescmd.Command(writescmd.Options{Env: mc.Env, Now: m.Now, Store: store}),
			ledgercmd.Command(ledgercmd.Options{Env: mc.Env, Now: m.Now, Store: store}),
			proposalscmd.Command(proposalscmd.Options{Env: mc.Env, Clients: m.Clients, Now: m.Now, Enabled: enabled, Tools: mc.Tools, Store: store}),
			settingscmd.Command(mc.Env, m.Guard),
			bearer.Command(mc.Env),
			secretscmd.Command(mc.Env),
			dbcmd.Command(mc.Env, m.Now),
			backup.BackupCommand(),
			backup.RestoreCommand(),
			catalogCommand(mc),
		},
	}
}

func AttentionSources() []attention.Source { return []attention.Source{attentionsources.Proposals()} }

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "sessions", Title: "Sessions", Page: "/settings/auth", Keys: []string{oidc.KeyIdleDays, oidc.KeyMaxDays}},
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
			return connweb.Watch(ctx, d)
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
