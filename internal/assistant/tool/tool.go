package tool

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

type Args map[string]any

func (a Args) Get(name string) (any, bool) {
	v, ok := a[name]
	return v, ok
}

type Handler func(ctx context.Context, tc *Context, args Args) (any, error)

type Flags struct {
	UntrustedOutput bool
	PrivateData     bool
	ExternalEffect  bool
	Cost            bool
	Web             bool
}

type Approval struct {
	Apply  Handler
	Label  string
	Icon   string
	Module string
	Detail string
}

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     Handler
	Effect      Effect
	Undo        string
	Flags       Flags
	Annotated   bool
	Contexts    Kinds
	Server      bool
	Parallel    bool
	Approval    *Approval
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
	Undoers     *writes.Registry
}

// Park stops the run and waits for the operator. The loop leaves the tool call
// open and finishes the run as parked; an answer resumes it.
type Park struct{ Question string }

func (p *Park) Error() string { return "waiting for the operator: " + p.Question }

type Tainted struct{ Value any }
