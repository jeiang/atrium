package coremodule

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/assistant/writes/writescmd"
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
		Name: module.Core,
		Core: true,
		Pages: []web.Page{
			{Module: about.Module(m.Version), Templates: about.Templates()},
			{Module: mailpages.PauseModule},
			{Module: reviewpages.Module(reviewpages.Options{Writes: toolset.Undoers(mc.Env), Enabled: enabled}), Templates: reviewpages.Templates()},
			{Start: startOps},
			{Module: startEmbed},
			{Module: integrations.Module, Templates: integrations.Templates()},
			{Module: settingspages.Module, Templates: settingspages.Templates()},
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
	ctx, cancel := context.WithCancel(context.Background())
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
