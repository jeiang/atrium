package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/message"
)

type feed struct {
	env config.Env
}

func (f feed) cacheDir() (string, error) {
	cfg, err := config.Load(f.env)
	return cfg.CacheDir, err
}

func (feed) Message(ctx context.Context, q db.Querier, id int64) (*communications.Message, error) {
	return message.Summary(ctx, q, id)
}

func (feed) Messages(ctx context.Context, q db.Querier, query communications.Query) ([]communications.Message, error) {
	return message.Summaries(ctx, q, query)
}

func (feed) Body(ctx context.Context, q db.Querier, id int64) (string, error) {
	return message.Body(ctx, q, id)
}

func (f feed) Parts(ctx context.Context, q db.Querier, id int64) ([]communications.Part, error) {
	dir, err := f.cacheDir()
	if err != nil {
		return nil, err
	}
	return message.Parts(ctx, q, dir, id)
}

func (f feed) Part(ctx context.Context, q db.Querier, id int64, part string, limit int) ([]byte, error) {
	dir, err := f.cacheDir()
	if err != nil {
		return nil, err
	}
	return message.PartData(ctx, q, dir, id, part, limit)
}
