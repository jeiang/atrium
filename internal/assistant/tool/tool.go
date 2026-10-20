package tool

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type Handler func(ctx context.Context, tc *Context, args py.Dict) (any, error)

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     Handler
	Effect      Effect
	Contexts    Kinds
}

type Context struct {
	DB          *db.DB
	Cfg         config.Config
	Env         config.Env
	Kind        Kind
	ThreadID    int64
	RunID       int64
	AllowedURLs map[string]struct{}
	Embed       func(ctx context.Context, texts []string) ([][]float64, error)
	Now         func() float64
	Client      *anthropic.Client
	Progress    *progress.Writer
}
