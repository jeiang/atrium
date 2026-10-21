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

func makeClient(ctx context.Context, q db.Querier, env config.Env) (*notion.Client, error) {
	token, err := config.NotionToken(env)
	if err != nil {
		return nil, tool.NotConfigured(err.Error())
	}
	client := NewClient(token)
	return client, client.Configure(ctx, q)
}

func notionError(err error) error {
	var failure *notion.Error
	if errors.As(err, &failure) {
		return tool.Error("notion: " + err.Error())
	}
	return err
}

func str(d py.Dict, key string) string {
	s, _ := dictGet(d, key).(string)
	return s
}

func dictGet(d py.Dict, key string) any {
	v, _ := d.Get(key)
	return v
}

func required(args py.Dict, name string) (string, error) {
	text, _, err := tool.TextArg(args, name, true)
	return text, err
}
