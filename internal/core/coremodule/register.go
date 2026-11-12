package coremodule

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

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
	"github.com/jeiang/atrium/internal/kb/kbcmd"
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
	"github.com/jeiang/atrium/internal/proposals/proposalscmd"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/secrets/secretscmd"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/writes/writescmd"
)

type Module struct {
	Version string
	Guard   settingscmd.Guard
	Clients func(config.Config) mail.OpenFunc
	Now     func() float64
}

func (m Module) Declare(mc module.Context) module.Declaration {
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
		Metrics:           slices.Concat(collectors.Metrics(), opsmetrics.Metrics(), passrun.Metrics()),
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
			{Start: func(ctx context.Context, d *web.Deps) error { return startOps(ctx, d, mc.Catalog()) }},
			{Module: startEmbed},
			{Module: actionlinkpages.Module, Templates: actionlinkpages.Templates()},
			{Module: web.CoreModule, Nav: []web.NavItem{
				{Label: "Sign-in", URL: "/settings/auth", Page: "auth", Settings: true, Primary: true, Order: 50},
			}},
			connweb.Page(),
		},
		Groups: SettingGroups(),
		Commands: []*cli.Command{
			writescmd.Command(writescmd.Options{Env: mc.Env, Now: m.Now, Store: store}),
			ledgercmd.Command(ledgercmd.Options{Env: mc.Env, Now: m.Now, Store: store}),
			proposalscmd.Command(proposalscmd.Options{Env: mc.Env, Clients: m.Clients, Now: m.Now, Enabled: enabled, Tools: mc.Tools, Store: store}),
			settingscmd.Command(mc.Env, m.Guard),
			bearer.Command(mc.Env),
			secretscmd.Command(mc.Env),
			dbcmd.Command(mc.Env, m.Now),
			kbcmd.Command(mc.Env),
			backup.BackupCommand(),
			backup.RestoreCommand(),
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

func startOps(ctx context.Context, d *web.Deps, cat *module.Catalog) error {
	stop, err := ops.Start(ctx, d.DB, d.Env, cat.Metrics(), cat.Checks())
	if err != nil {
		log.Error("ops listener did not start", logx.Err, err.Error())
		return nil
	}
	d.OnClose(stop)
	return nil
}

const embedEvery = time.Minute

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

func startEmbed(_ chi.Router, d *web.Deps) {
	ctx, cancel := context.WithCancel(d.Ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			more, err := mail.ReembedStep(ctx, d.DB)
			if err != nil && ctx.Err() == nil {
				log.Warn("embedding index rebuild failed", logx.Err, err.Error())
			}
			if more {
				continue
			}
			if err := kbPending(ctx, d.DB); err != nil && ctx.Err() == nil {
				log.Warn("knowledge embedding failed", logx.Err, err.Error())
			}
			select {
			case <-ctx.Done():
			case <-time.After(embedEvery):
			}
		}
	}()
	d.OnClose(func() {
		cancel()
		<-done
	})
}
