package application

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
)

const (
	PermissionsName       application.Name = "review.permissions"
	PermissionsUpdateName application.Name = "review.permissions-update"
)

type PermissionsInput struct{}

type PermissionRule struct {
	Key       string   `json:"key"`
	Channel   string   `json:"channel"`
	Situation string   `json:"situation"`
	Value     string   `json:"value"`
	Source    string   `json:"source"`
	Floor     string   `json:"floor"`
	Effective string   `json:"effective"`
	Choices   []string `json:"choices"`
}

type Permissions struct {
	Rules           []PermissionRule `json:"rules"`
	PendingRequests int              `json:"pending_requests"`
}

type PermissionChange struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type PermissionsUpdateInput struct {
	Changes []PermissionChange `json:"changes"`
}

type PermissionsUpdated struct {
	Keys []string `json:"keys"`
}

func DescribePermissions() []application.Definition {
	operator := []application.Authority{application.Operator}
	return []application.Definition{
		application.Describe[PermissionsInput, Permissions](PermissionsName, application.Query, operator, application.NoEffect, false),
		application.Describe[PermissionsUpdateInput, PermissionsUpdated](PermissionsUpdateName, application.Command, operator, application.NoEffect, true),
	}
}

func permissionKeys() []string {
	channels, situations := registry.PermissionAxes()
	out := make([]string, 0, len(channels)*len(situations))
	for c := range channels {
		for s := range situations {
			out = append(out, registry.PermissionKey(c, s))
		}
	}
	return out
}

func (s *Service) Permissions(ctx context.Context, in PermissionsInput) (application.Read[Permissions], error) {
	return read(ctx, s, PermissionsName, []string{state.Settings, state.Review}, in, func() (Permissions, error) {
		reg := settings.From(ctx)
		channels, situations := registry.PermissionAxes()
		out := Permissions{Rules: make([]PermissionRule, 0, len(channels)*len(situations))}
		for c := range channels {
			for si := range situations {
				key := registry.PermissionKey(c, si)
				found, err := reg.Resolve(ctx, s.db.Read, key)
				if err != nil {
					return Permissions{}, err
				}
				def, _ := reg.Lookup(key)
				floor := registry.PermissionFloor(c, si)
				effective := floor
				if decision, ok := registry.ParseDecision(found.Value); ok {
					effective = max(decision, floor)
				}
				out.Rules = append(out.Rules, PermissionRule{
					Key: key, Channel: channels[c], Situation: situations[si], Value: found.Value, Source: string(found.Origin),
					Floor: floor.String(), Effective: effective.String(), Choices: slices.Clone(def.Choices),
				})
			}
		}
		err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE kind = ? AND status = ?", permission.Kind, proposals.Pending).Scan(&out.PendingRequests)
		return out, err
	}, nil)
}

func (s *Service) UpdatePermissions(ctx context.Context, in PermissionsUpdateInput) (application.Change[PermissionsUpdated], error) {
	if len(in.Changes) == 0 {
		return application.Change[PermissionsUpdated]{}, invalid("changes", "Name at least one permission.")
	}
	known := permissionKeys()
	values := make([]settingsapp.Value, 0, len(in.Changes))
	for _, c := range in.Changes {
		if !slices.Contains(known, c.Key) {
			return application.Change[PermissionsUpdated]{}, invalid("changes", "Unknown permission "+c.Key+".")
		}
		value := strings.TrimSpace(c.Value)
		values = append(values, settingsapp.Value{Key: c.Key, Value: value, Unset: value == ""})
	}
	done, err := settingsapp.Web(s.db).Set(ctx, settingsapp.SetInput{Values: values})
	if e, ok := errors.AsType[*application.Error](err); ok && e.Code == application.Invalid && len(e.Fields) == 0 {
		return application.Change[PermissionsUpdated]{}, &application.Error{Code: e.Code, Message: e.Message, Fields: []application.Field{{Name: "changes", Code: string(application.Invalid), Message: e.Message}}, Cause: err}
	}
	if err != nil {
		return application.Change[PermissionsUpdated]{}, err
	}
	return application.Change[PermissionsUpdated]{Data: PermissionsUpdated{Keys: done.Data.Keys}, AuditID: done.AuditID}, nil
}
