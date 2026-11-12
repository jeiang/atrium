package module

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settingspage"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

type Context struct {
	Current  func() *Snapshot
	Evaluate func(context.Context, db.Querier) (*Snapshot, error)
	Catalog  func() *Catalog
	Env      config.Env
}

func (c Context) Enabled(name string) bool {
	snap := c.Current()
	return snap != nil && snap.Enabled(name)
}

func (c Context) Tools() (*registry.Registry, error) {
	snap := c.Current()
	if snap == nil {
		return nil, ErrNotEvaluated
	}
	return snap.Registry()
}

type Declaration struct {
	Name              string
	Core              bool
	Needs             []string
	Requires          []Requirement
	Settings          []settings.Def
	Host              []settings.HostDef
	Groups            []settings.Group
	Sections          []settingspage.Section
	Pages             []web.Page
	Commands          []*cli.Command
	Leaves            map[string]Leaf
	Tools             []ToolDefinition
	ProposeKinds      []proposals.Definition
	Effects           func(EffectDeps) Effects
	Connections       []connections.Definition
	AccountSync       *connections.AccountSync
	AccountCredential config.AccountCredentialFunc
	ProposalCreated   proposals.CreatedHook
	ConnectionNotice  connections.Notifier
	Attention         []attention.Source
	Jobs              []scheduler.Definition
	Metrics           []ops.Metric
	Checks            []ops.Check
}

type EffectDeps struct {
	DB        *db.DB
	Ledger    *ledger.Store
	Env       config.Env
	Open      mail.OpenFunc
	Syncer    *mailsync.Syncer
	Now       func() float64
	Resume    func(context.Context, int64) error
	Tools     registry.Source
	Approvals []tool.Tool
}

type Effects struct {
	Proposals []proposals.Definition
	Undo      []ledger.UndoDefinition
	Resolve   []ledger.ResolveDefinition
	Sync      proposals.SyncHook
}

type Builder interface{ Declare(mc Context) Declaration }
