package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	commitmentsapp "github.com/jeiang/atrium/internal/commitments/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/review/hub"
	"github.com/jeiang/atrium/internal/web"
)

type ReviewOptions struct {
	Application func(*web.Deps) *mailapp.Service
	Clients     func(config.Config) mail.OpenFunc
	Tools       registry.Source
}

func recoverDrafts(ctx context.Context, d *db.DB, cfg config.Config) error {
	_, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx)
	return err
}

func ReviewServices(o ReviewOptions) func(*web.Deps) reviewpages.Services {
	return func(d *web.Deps) reviewpages.Services {
		rt := hub.Compose(hub.ComposeOptions{Tools: o.Tools})(d)
		draftStore := &drafts.Store{DB: d.DB, Cfg: d.Cfg, Ledger: rt.Ledger, Open: o.Clients(d.Cfg), Now: rt.Now}
		return reviewpages.Services{
			Mail:   o.Application(d).WithSender(draftStore).WithCommitments(commitmentsapp.New(d.DB, rt.Ledger, rt.Now)),
			Review: rt.Review,
		}
	}
}
