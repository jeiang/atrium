package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/apps/mail/drafts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/review/hub"
	"github.com/jeiang/atrium/internal/web"
)

func recoverDrafts(ctx context.Context, d *db.DB, cfg config.Config) error {
	_, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx)
	return err
}

func (m Module) reviewServices(tools registry.Source) func(*web.Deps) reviewpages.Services {
	return func(d *web.Deps) reviewpages.Services {
		rt := hub.Compose(hub.ComposeOptions{Tools: tools})(d)
		draftStore := &drafts.Store{DB: d.DB, Cfg: d.Cfg, Ledger: rt.Ledger, Open: m.opener(d.Cfg), Now: rt.Now}
		return reviewpages.Services{
			Mail:   mailPageService(d).WithSender(draftStore),
			Review: rt.Review,
		}
	}
}
