package sdk

import (
	"log/slog"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type (
	Module       = module.Module
	Registration = module.Registration
	Context      = module.Context
	Requirement  = module.Requirement
	Probe        = module.Probe
	Check        = module.Check
	Leaf         = module.Leaf

	Command = cli.Command
	Args    = cli.Args
	Opt     = cli.Opt
	Env     = cli.Env
	Handler = cli.Handler

	Page     = web.Page
	PageDeps = web.Deps

	Tools       = registry.Registry
	Tool        = tool.Tool
	ToolContext = tool.Context
	ToolHandler = tool.Handler
	ToolArgs    = py.Dict
	Effect      = tool.Effect
	Kind        = tool.Kind
	Kinds       = tool.Kinds

	Setting      = settings.Def
	SettingType  = settings.Type
	SettingClass = settings.Class
	SettingScope = settings.Scope

	DB        = db.DB
	Querier   = db.Querier
	Migration = db.ModuleMigration
	Config    = config.Config
)

const (
	ExitOK    = cli.ExitOK
	ExitError = cli.ExitError
	ExitUsage = cli.ExitUsage

	Read    = tool.Read
	Write   = tool.Write
	Propose = tool.Propose
	Notify  = tool.Notify

	Chat    = tool.Chat
	Cron    = tool.Cron
	Nightly = tool.Nightly
	Sub     = tool.Sub

	String = settings.String
	Int    = settings.Int
	Float  = settings.Float
	Bool   = settings.Bool
	Enum   = settings.Enum

	UI       = settings.UI
	Advanced = settings.Advanced
	Secret   = settings.Secret

	Instance = settings.Instance
	Thread   = settings.Thread
)

var Settings = settings.Default

var modules []Module

func Register(name string, m Module) {
	settings.MustRegister(module.FlagDef(name))
	modules = append(modules, m)
}

func Modules() []Module { return slices.Clone(modules) }

func RegisterSetting(def Setting) { settings.MustRegister(def) }

func RegisterMigrations(module string, ms ...Migration) { db.RegisterModuleMigrations(module, ms...) }

func RequireSetting(key string) Requirement { return module.Setting(key) }

func OpenDB(env *Env) (*DB, Config, error) {
	cfg, err := config.Load(config.OS)
	if err != nil {
		return nil, cfg, err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, cfg, err
	}
	if err := config.Migrate(env.Ctx, d, cfg); err != nil {
		_ = d.Close()
		return nil, cfg, err
	}
	return d, cfg, nil
}

func Logger(component string) *slog.Logger { return logx.Logger(component) }

func Fail(env *Env, err error) int { return cli.Fail(env, err) }
