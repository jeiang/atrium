package module

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	Core          = "core"
	Mail          = "mail"
	Calendar      = "calendar"
	Notion        = "notion"
	Media         = "media"
	Assistant     = "assistant"
	Notifications = "notifications"
	Grafana       = "grafana"
	Contacts      = "contacts"
)

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
	Name       string
	Key        string
	Host       bool
	Connection bool
	Check      Check
}

func HostSetting(key string) Requirement {
	return Requirement{Name: key, Key: key, Host: true, Check: func(ctx context.Context, p Probe) (string, error) {
		value := config.From(ctx).HostValue(key)
		if strings.TrimSpace(value) == "" {
			return key + " is not set", nil
		}
		if err := p.Settings.ValidateHost(key, value); err != nil {
			return key + " " + err.Error(), nil
		}
		return "", nil
	}}
}

func Connection(id string) Requirement {
	return Requirement{Name: id, Key: id, Connection: true, Check: func(ctx context.Context, p Probe) (string, error) {
		lease, err := connections.OpenRead(ctx, p.Q, config.From(ctx).Host(), connections.ID(id))
		if err != nil {
			var unknown connections.UnknownID
			if errors.As(err, &unknown) {
				return "the " + id + " connection is not configured", nil
			}
			return "", err
		}
		defer lease.Close()
		if _, err := lease.Endpoint("url"); err != nil {
			return err.Error(), nil
		}
		return "", nil
	}}
}

func Setting(key string) Requirement {
	return Requirement{Name: key, Key: key, Check: func(ctx context.Context, p Probe) (string, error) {
		got, err := p.Settings.Resolve(ctx, p.Q, key)
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
	return settings.Value(settings.UI, settings.Def{
		Key:     FlagKey(name),
		Module:  Core,
		Type:    settings.Bool,
		Default: "false"})
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
	Name  string
	State State

	Unmet []Unmet
}

var ErrNotEvaluated = errors.New("module: states are not evaluated yet")

type DuplicateModule string

func (e DuplicateModule) Error() string { return "module: duplicate module " + string(e) }

type DuplicateCommand string

func (e DuplicateCommand) Error() string { return "module: duplicate command " + string(e) }

type ToolSettingCollision struct{ Module, Key string }

func (e *ToolSettingCollision) Error() string {
	return "module: " + e.Module + " declares the setting " + e.Key + ", which the tool registry owns"
}

type UnknownDependency struct {
	Module     string
	Dependency string
}

func (e *UnknownDependency) Error() string {
	return "module: " + e.Module + " needs " + e.Dependency + ", which is not registered"
}

type UnknownLeaf struct {
	Module string
	Path   string
}

func (e *UnknownLeaf) Error() string {
	return "module: " + e.Module + " has no command " + e.Path
}

type DependencyCycle struct{ Modules []string }

func (e *DependencyCycle) Error() string {
	return "module: dependency cycle among " + strings.Join(e.Modules, ", ")
}

type InvalidLeaf struct{ Module, Path string }

func (e *InvalidLeaf) Error() string {
	return "module: " + e.Module + " leaf " + e.Path + " sets both Always and Needs"
}

type InvalidGroup struct{ Module, Group string }

func (e *InvalidGroup) Error() string {
	return "module: " + e.Module + " has an invalid settings group " + strconv.Quote(e.Group)
}

type InvalidPage struct{ Module string }

func (e *InvalidPage) Error() string {
	return "module: " + e.Module + " has a page with no Module"
}

type InvalidCore struct{ Module, Reason string }

func (e *InvalidCore) Error() string {
	if e.Module == "" {
		return "module: " + e.Reason
	}
	if e.Reason == "" {
		return "module: core module " + e.Module + " cannot declare needs or requirements"
	}
	return "module: core module " + e.Module + " " + e.Reason
}

type InvalidDeclaration struct{ Module, Field string }

func (e *InvalidDeclaration) Error() string {
	if e.Module == "" {
		return "module: declaration has no " + e.Field
	}
	return "module: " + e.Module + " has an invalid " + e.Field
}

type DeclarationError struct {
	Module string
	Field  string
	Err    error
}

func (e *DeclarationError) Error() string {
	return "module: " + e.Module + " " + e.Field + ": " + e.Err.Error()
}

func (e *DeclarationError) Unwrap() error { return e.Err }

type DuplicateMetric struct{ Name string }

func (e *DuplicateMetric) Error() string { return "module: duplicate metric " + e.Name }

type DuplicateCheck struct{ Name string }

func (e *DuplicateCheck) Error() string { return "module: duplicate check " + e.Name }

type DuplicateTool string

func (e DuplicateTool) Error() string { return "module: duplicate tool " + string(e) }

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
