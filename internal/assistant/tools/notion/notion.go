package notiontools

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
)

func makeClient(ctx context.Context, q db.Querier, env config.Env) (*notion.Client, error) {
	client, err := notion.NewClient(ctx, q, env)
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return nil, tool.NotConfigured(err.Error())
	}
	return client, err
}

func notionError(err error) error {
	return tool.Domain[*notion.Error, tool.Error](err)
}

func str(d any, key string) string {
	s, _ := value(d, key).(string)
	return s
}

func value(d any, key string) any {
	switch v := d.(type) {
	case map[string]any:
		return v[key]
	case getter:
		x, _ := v.Get(key)
		return x
	}
	return nil
}

type getter interface{ Get(string) (any, bool) }

func dictGet(d getter, key string) any {
	v, _ := d.Get(key)
	return v
}

func required(args tool.Args, name string) (string, error) {
	text, _, err := tool.TextArg(args, name, true)
	return text, err
}
