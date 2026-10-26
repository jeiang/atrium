package accountpages

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/web"
)

const reembedEvery = time.Minute

var log = logx.Logger("atrium.mail.embed")

func reembed(d *web.Deps) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-d.Stopping
		cancel()
	}()
	for ctx.Err() == nil {
		more, err := mail.ReembedStep(ctx, d.DB, stamp())
		if err != nil && ctx.Err() == nil {
			log.Warn("embedding index rebuild failed", logx.Err, err.Error())
		}
		if more {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(reembedEvery):
		}
	}
}
