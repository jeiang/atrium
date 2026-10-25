package module

import (
	"context"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

const (
	Core          = "core"
	Mail          = "mail"
	Calendar      = "calendar"
	Notion        = "notion"
	Money         = "money"
	Media         = "media"
	Assistant     = "assistant"
	Notifications = "notifications"
	Grafana       = "grafana"
	Contacts      = "contacts"
)

type Module interface {
	Register(mc Context) Registration
}

type Context struct {
	Current func() *Snapshot
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

type Registration struct {
	Name     string
	Core     bool
	Needs    []string
	Soft     []string
	Requires []Requirement
	Pages    []web.Page
	Commands []*cli.Command
	Leaves   map[string]Leaf
	Tools    func(r *registry.Registry) error
}

type Leaf struct {
	Always bool
	Needs  []string
}

type Probe struct {
	Settings *settings.Registry
	Q        db.Querier
	Env      config.Env
}

type Check func(ctx context.Context, p Probe) (reason string, err error)

type Requirement struct {
	Name  string
	Key   string
	Check Check
}

func Setting(key string) Requirement {
	return Requirement{Name: key, Key: key, Check: func(ctx context.Context, p Probe) (string, error) {
		got, err := p.Settings.Resolve(ctx, p.Q, key, 0)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(got.Value) == "" {
			return key + " is not set", nil
		}
		err = p.Settings.Validate(key, got.Value)
		var invalid settings.Invalid
		if errors.As(err, &invalid) {
			return invalid.Error(), nil
		}
		return "", err
	}}
}

func FlagKey(name string) string { return name + "_enabled" }

func FlagDef(name string) settings.Def {
	return settings.Def{
		Key:     FlagKey(name),
		Module:  Core,
		Type:    settings.Bool,
		Default: "false",
		Class:   settings.UI,
		Restart: true,
		Scopes:  []settings.Scope{settings.Instance},
	}
}

type State string

const (
	Enabled       State = "enabled"
	Off           State = "off"
	NeedsSettings State = "needs-settings"
)

type Unmet struct {
	Name   string
	Reason string
}

type Status struct {
	Name   string
	State  State
	Reason string
	Unmet  []Unmet
}

var ErrNotEvaluated = errors.New("module: states are not evaluated yet")

type DuplicateModule string

func (e DuplicateModule) Error() string { return "module: duplicate module " + string(e) }

type DuplicateCommand string

func (e DuplicateCommand) Error() string { return "module: duplicate command " + string(e) }

type FlagNotRegistered string

func (e FlagNotRegistered) Error() string {
	return "module: setting " + string(e) + " is not registered"
}

type UnknownDependency struct {
	Module     string
	Dependency string
}

func (e *UnknownDependency) Error() string {
	return "module: " + e.Module + " needs " + e.Dependency + ", which is not registered before it"
}

type UnknownLeaf struct {
	Module string
	Path   string
}

func (e *UnknownLeaf) Error() string {
	return "module: " + e.Module + " has no command " + e.Path
}

type InvalidCore struct{ Module string }

func (e *InvalidCore) Error() string {
	return "module: core module " + e.Module + " cannot declare needs or requirements"
}

type NeedsSettingsError struct {
	Module string
	Unmet  []Unmet
}

func (e *NeedsSettingsError) Error() string {
	return e.Module + " needs settings: " + joinReasons(e.Unmet)
}

func joinReasons(unmet []Unmet) string {
	reasons := make([]string, len(unmet))
	for i, u := range unmet {
		reasons[i] = u.Reason
	}
	return strings.Join(reasons, "; ")
}
