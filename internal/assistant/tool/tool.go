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

// Handler runs one tool call. args is the decoded input object (py.JSONLoads). The result is a string, sent as
// is, or any value py.JSONDumps encodes. An Error or NotConfigured is shown to the model as the call's error;
// any other error is reported as "<name> failed: <class>" (see Raised).
type Handler func(ctx context.Context, tc *Context, args py.Dict) (any, error)

// Tool is one registry entry. Effect and Contexts have no defaults here: Python's defaults are Read and
// DefaultKinds, and a tool states them explicitly.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     Handler
	Effect      Effect
	Contexts    Kinds
}

// Context is ToolContext: what a handler may use during one run. Tools never touch the run journal directly.
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
