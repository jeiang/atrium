package mailmodule

import (
	"context"

	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/apps/mail/imapclient"
	"github.com/jeiang/atrium/internal/apps/mail/mailcmd"
	"github.com/jeiang/atrium/internal/apps/mail/run"
	"github.com/jeiang/atrium/internal/config"
)

func (m Module) mailCommands(raw config.Env) mailcmd.Options {
	env := raw
	if env == nil {
		env = config.OS
	}
	return mailcmd.Options{
		Load:    func() (config.Config, error) { return config.Load(env) },
		Clients: m.Clients,
		Now:     m.Now,
		Open: func(ctx context.Context) (*run.Pass, func(), error) {
			cfg, err := config.Load(env)
			if err != nil {
				return nil, nil, err
			}
			clients := m.Clients
			if clients == nil {
				clients = imapclient.Opener
			}
			return run.Open(ctx, env, cfg, clients(cfg), m.Now, m.classifiers)
		},
		Standalone: func(cfg config.Config, now func() float64) *mailapp.Service {
			return mailapp.New(nil, cfg, env, now).WithClassifiers(m.classifiers)
		},
		Application: mailCLIService(raw),
	}
}
