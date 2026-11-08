package notiontools

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/py"
)

var NewClient = notion.NewClient

func makeClient(ctx context.Context, q db.Querier) (*notion.Client, error) {
	token, err := notion.Token(ctx, q)
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return nil, tool.NotConfigured(err.Error())
	}
	if err != nil {
		return nil, err
	}
	client := NewClient(token)
	return client, client.Configure(ctx, q)
}

func notionError(err error) error {
	return tool.Domain[*notion.Error, tool.Error](err)
}

func str(d py.Dict, key string) string {
	s, _ := dictGet(d, key).(string)
	return s
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
