package coremodule

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/settingspage"
	"github.com/jeiang/atrium/internal/web"
)

type SettingsRequest struct {
	Group  string     `query:"group" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}

type SettingsPage struct {
	Items      []settingsapp.Setting `json:"items"`
	NextCursor *string               `json:"next_cursor" api:"nullable"`
}

type SettingKey struct {
	Key string `path:"key" json:"-"`
}

type SettingSet struct {
	Key   string `path:"key" json:"-"`
	Value string `json:"value,omitempty"`
}

type SettingChange struct {
	Keys []string `json:"keys"`
}

type Secrets struct {
	Secrets []settingsapp.SecretState `json:"secrets"`
}

type SecretSet struct {
	Name  string `path:"key" json:"-"`
	Value string `json:"value,omitempty" api:"writeonly"`
}

func (m Module) settingsOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return []application.Definition{
			settingsapp.DescribeRead(),
			settingsapp.DescribeSet(),
			settingsapp.DescribeUnset(),
			settingsapp.DescribeSecretStatus(),
			settingsapp.DescribeSecretSet(),
			settingsapp.DescribeSecretDelete(),
		}, append(m.settingsRoutes(nil), []application.Binding{
			{Kind: application.CLI, Target: "settings list", Uses: uses(settingsapp.ReadName)},
			{Kind: application.CLI, Target: "settings get", Uses: uses(settingsapp.ReadName)},
			{Kind: application.CLI, Target: "settings set", Uses: []application.Name{settingsapp.SetName, settingsapp.ReadName}},
			{Kind: application.CLI, Target: "settings unset", Uses: []application.Name{settingsapp.UnsetName, settingsapp.ReadName}},
			{Kind: application.CLI, Target: "secrets set", Uses: uses(settingsapp.SecretSetName)},
			{Kind: application.CLI, Target: "secrets delete", Uses: uses(settingsapp.SecretDeleteName)},
			{Kind: application.CLI, Target: "secrets status", Uses: uses(settingsapp.SecretStatusName)},
			{Kind: application.HTML, Target: "POST /settings/groups/{module}/{name}", Uses: []application.Name{settingsapp.SetName, settingsapp.ReadName}},
			{Kind: application.HTML, Target: "POST /settings/sections/{name}", Uses: []application.Name{settingsapp.SetName}},
		}...)
}

func valueField[T any](change application.Change[T], err error) (application.Change[T], error) {
	if e, ok := errors.AsType[*application.Error](err); ok && e.Code == application.Invalid && len(e.Fields) == 0 {
		return change, &application.Error{Code: e.Code, Message: e.Message, Fields: []application.Field{{Name: "value", Code: string(application.Invalid), Message: e.Message}}, Cause: err}
	}
	return change, err
}

func offered(ctx context.Context, key string) (settingspage.Section, bool) {
	set := module.SetFrom(ctx)
	if set == nil {
		return settingspage.Section{}, false
	}
	return set.OfferedSetting(key)
}

func notOffered(key string) error {
	return &application.Error{Code: application.Refused, Message: key + " is not offered on the settings pages and is not writable here"}
}

func (m Module) settingsRoutes(d *web.Deps) []application.Binding {
	svc := func() *settingsapp.Service { return settingsapp.Server(d.DB, d.Settings, m.Guard) }
	const area = "settings"
	refusable := []application.Code{application.Invalid, application.Refused}
	changed := func(change application.Change[settingsapp.Changed], err error) (application.Change[SettingChange], error) {
		return application.Change[SettingChange]{Data: SettingChange{Keys: change.Data.Keys}, AuditID: change.AuditID}, err
	}
	return []application.Binding{
		api.Query(d, settingsapp.DescribeRead(), application.APIShape{Area: area},
			"GET /settings", func(ctx context.Context, in SettingsRequest) (application.Read[SettingsPage], error) {
				var after string
				if in.Cursor.Set() {
					if err := in.Cursor.Last(&after); err != nil {
						return application.Read[SettingsPage]{}, err
					}
				}
				var keys []string
				for _, def := range settings.From(ctx).Defs() {
					if _, ok := offered(ctx, def.Key); ok && (in.Group == "" || def.Module == in.Group) && def.Key > after {
						keys = append(keys, def.Key)
					}
				}
				slices.SortFunc(keys, cmp.Compare)
				page := SettingsPage{Items: []settingsapp.Setting{}}
				if len(keys) > int(in.Limit) {
					keys = keys[:in.Limit]
					page.NextCursor = in.Cursor.Next(keys[len(keys)-1])
				}
				read, err := svc().Read(ctx, settingsapp.ReadInput{Keys: keys})
				if err != nil {
					return application.Read[SettingsPage]{}, err
				}
				page.Items = append(page.Items, read.Data.Rows...)
				return application.Read[SettingsPage]{Revision: read.Revision, Data: page}, nil
			}),
		api.Command(d, settingsapp.DescribeSet(), application.APIShape{Area: area, Errors: refusable},
			"PUT /settings/{key}", func(ctx context.Context, in SettingSet) (application.Change[SettingChange], error) {
				section, ok := offered(ctx, in.Key)
				if !ok {
					if err := settingsapp.CheckRead(ctx, in.Key); err != nil {
						return application.Change[SettingChange]{}, err
					}
					return application.Change[SettingChange]{}, notOffered(in.Key)
				}
				return valueField(changed(svc().Set(ctx, settingsapp.SetInput{Values: []settingsapp.Value{{Key: in.Key, Value: in.Value}}, Validate: section.Validate})))
			}),
		api.Command(d, settingsapp.DescribeUnset(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable},
			"DELETE /settings/{key}", func(ctx context.Context, in SettingKey) (application.Change[SettingChange], error) {
				section, ok := offered(ctx, in.Key)
				switch {
				case !ok:
					if err := settingsapp.CheckRead(ctx, in.Key); err != nil {
						return application.Change[SettingChange]{}, err
					}
					return application.Change[SettingChange]{}, notOffered(in.Key)
				case section.Validate != nil:
					return valueField(changed(svc().Set(ctx, settingsapp.SetInput{Values: []settingsapp.Value{{Key: in.Key, Unset: true}}, Validate: section.Validate})))
				}
				return changed(svc().Unset(ctx, settingsapp.UnsetInput{Key: in.Key}))
			}),
		api.Query(d, settingsapp.DescribeSecretStatus(), application.APIShape{Area: area},
			"GET /settings/secrets", func(ctx context.Context, _ struct{}) (application.Read[Secrets], error) {
				read, err := svc().SecretStatus(ctx, settingsapp.SecretStatusInput{})
				return application.Read[Secrets]{Revision: read.Revision, Data: Secrets{Secrets: read.Data}}, err
			}),
		api.Command(d, settingsapp.DescribeSecretSet(), application.APIShape{Area: area, Errors: refusable},
			"PUT /settings/secrets/{key}", func(ctx context.Context, in SecretSet) (application.Change[settingsapp.SecretState], error) {
				return valueField(svc().SetSecret(ctx, settingsapp.SecretSetInput{Name: in.Name, Value: in.Value}))
			}),
		api.Command(d, settingsapp.DescribeSecretDelete(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable},
			"DELETE /settings/secrets/{key}", func(ctx context.Context, in SettingKey) (application.Change[settingsapp.SecretState], error) {
				return svc().DeleteSecret(ctx, settingsapp.SecretDeleteInput{Name: in.Key})
			}),
	}
}
