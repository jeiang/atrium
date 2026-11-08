package coremodule

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/actionlink"
	actionlinkpages "github.com/jeiang/atrium/internal/actionlink/pages"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/assistant/writes/writescmd"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/auth/loginlink"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbcmd"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	mailpages "github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/module"
	_ "github.com/jeiang/atrium/internal/obs/opsmetrics"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/proposals/proposalscmd"
	"github.com/jeiang/atrium/internal/secrets/secretscmd"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
	"github.com/jeiang/atrium/internal/settings/settingspages"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Version string
	Guard   settingscmd.Guard
	Clients func(config.Config) mail.OpenFunc
	Now     func() float64
}

func (m Module) Register(mc module.Context) module.Registration {
	enabled := func(ctx context.Context, q db.Querier, name string) (bool, error) {
		snap, err := mc.Evaluate(ctx, q)
		if err != nil {
			return false, err
		}
		return snap.Enabled(name), nil
	}
	return module.Registration{
		Name:      module.Core,
		Core:      true,
		Attention: []attention.Source{attentionsources.Proposals()},
		Pages: []web.Page{
			{Module: about.Module(m.Version), Templates: about.Templates(), Nav: []web.NavItem{
				{Label: "About", URL: "/about", Icon: "file-text", Page: "about", Footer: true, Order: 10},
			}},
			{Module: mailpages.PauseModule},
			{Module: reviewpages.Module(reviewpages.Options{Writes: toolset.Undoers(mc.Env), Enabled: enabled}), Templates: reviewpages.Templates(), Public: actionlink.PublicCheck, Nav: []web.NavItem{
				{Group: "Review", Label: "Proposals", URL: "/proposals", Icon: "list-checks", Page: "proposals", Order: 20, Badge: true},
				{Group: "Review", Label: "Activity", URL: "/activity", Icon: "history", Page: "activity", Order: 20},
				{Group: "Mail", Label: "Drafts", URL: "/drafts", Icon: "mail", Page: "drafts", Order: 30},
				{Group: "Life", Label: "Loops", URL: "/loops", Icon: "list-checks", Page: "loops", Order: 40},
				{Group: "Life", Label: "Watches", URL: "/watches", Icon: "package", Page: "watches", Order: 40},
			}},
			{Start: startOps},
			{Module: startEmbed},
			{Module: integrations.Module, Templates: integrations.Templates(), Nav: []web.NavItem{
				{Label: "Integrations", URL: "/settings/integrations", Page: "integrations", Settings: true, Order: 70},
			}},
			{Module: settingspages.Module, Templates: settingspages.Templates(), Nav: []web.NavItem{
				{Label: "Configuration", URL: "/settings/config", Page: "config", Settings: true, Order: 110},
				{Label: "Audit", URL: "/settings/audit", Page: "audit", Settings: true, Order: 120},
			}},
			{Module: actionlinkpages.Module, Templates: actionlinkpages.Templates(), Nav: []web.NavItem{
				{Label: "Phone actions", URL: "/settings/phone-actions", Page: "phone_actions", Settings: true, Order: 61},
			}},
			{Nav: []web.NavItem{
				{Label: "Sign-in", URL: "/settings/auth", Page: "auth", Settings: true, Primary: true, Auth: true, Order: 90},
			}},
		},
		Commands: []*cli.Command{
			writescmd.Command(mc.Env),
			proposalscmd.Command(proposalscmd.Options{Env: mc.Env, Clients: m.Clients, Now: m.Now, Enabled: enabled}),
			settingscmd.Command(mc.Env, m.Guard),
			loginlink.Command(),
			secretscmd.Command(mc.Env),
			dbcmd.Command(mc.Env),
			backup.BackupCommand(),
			backup.RestoreCommand(),
		},
	}
}

var log = logx.Logger("atrium.core")

func startOps(ctx context.Context, d *web.Deps) error {
	stop, err := ops.Start(ctx, d.DB, d.Env)
	if err != nil {
		log.Error("ops listener did not start", logx.Err, err.Error())
		return nil
	}
	d.OnClose(stop)
	return nil
}

const embedEvery = time.Minute

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
