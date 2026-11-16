package coremodule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"runtime"
	"runtime/debug"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/web"
)

const (
	ShellName application.Name = "core.shell"
	AboutName application.Name = "core.about"
)

type Shell struct {
	web.ShellFrame
	Modules []ModuleState `json:"modules"`
	Version string        `json:"version"`
}

type ModuleState struct {
	Name   string `json:"name"`
	State  string `json:"state" api:"enum=enabled|off|needs-settings|unavailable"`
	Reason string `json:"reason,omitempty"`
}

type AppAbout struct {
	Version       string        `json:"version"`
	Build         string        `json:"build,omitempty"`
	GoVersion     string        `json:"go_version"`
	APIVersion    int           `json:"api_version"`
	SchemaVersion int           `json:"schema_version"`
	Modules       []ModuleState `json:"modules"`
}

type SettingsSchema struct {
	Pages []SettingsSchemaPage `json:"pages"`
}

type SettingsSchemaPage struct {
	Kind   string                `json:"kind" api:"enum=group|section|module"`
	Module string                `json:"module"`
	Name   string                `json:"name"`
	Title  string                `json:"title"`
	URL    string                `json:"url"`
	Fields []SettingsSchemaField `json:"fields"`
}

type SettingsSchemaField struct {
	Key       string          `json:"key"`
	Label     string          `json:"label"`
	Module    string          `json:"module"`
	Type      string          `json:"type" api:"enum=string|int|float|bool|enum"`
	Multi     bool            `json:"multi"`
	Choices   []SettingChoice `json:"choices"`
	Min       *int64          `json:"min,omitempty"`
	Max       *int64          `json:"max,omitempty"`
	Default   *string         `json:"default"`
	Value     *string         `json:"value"`
	Source    *string         `json:"source"`
	PinSource string          `json:"pin_source,omitempty"`
	Class     string          `json:"class"`
	Editable  bool            `json:"editable"`
	Secret    bool            `json:"secret"`
}

type SettingChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func describeCoreRead[O any](name application.Name) application.Definition {
	return application.Describe[struct{}, O](name, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func (m Module) readModelOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{describeCoreRead[Shell](ShellName), describeCoreRead[AppAbout](AboutName)}, m.readModelRoutes(nil)
}

func (m Module) readModelRoutes(d *web.Deps) []application.Binding {
	settingsSvc := func() *settingsapp.Service { return settingsapp.Server(d.DB, d.Settings, m.Guard) }
	return []application.Binding{
		api.Query(d, describeCoreRead[Shell](ShellName), application.APIShape{Area: "home", Response: reflect.TypeFor[Shell]()},
			"GET /shell", func(ctx context.Context, _ struct{}) (application.Read[Shell], error) {
				return revised(Shell{ShellFrame: d.Render.Shell(ctx), Modules: moduleStates(ctx), Version: m.Version})
			}),
		api.Query(d, describeCoreRead[AppAbout](AboutName), application.APIShape{Area: "settings", Response: reflect.TypeFor[AppAbout]()},
			"GET /about", func(ctx context.Context, _ struct{}) (application.Read[AppAbout], error) {
				schema, err := db.SchemaVersion(ctx, d.DB.Read)
				if err != nil {
					return application.Read[AppAbout]{}, err
				}
				return revised(AppAbout{Version: m.Version, Build: buildRevision(), GoVersion: runtime.Version(), APIVersion: 1, SchemaVersion: schema, Modules: moduleStates(ctx)})
			}),
		api.Query(d, settingsapp.DescribeRead(), application.APIShape{Area: "settings", ID: "settings.schema", Response: reflect.TypeFor[SettingsSchema]()},
			"GET /settings/schema", func(ctx context.Context, _ struct{}) (application.Read[SettingsSchema], error) {
				return settingsSchema(ctx, settingsSvc())
			}),
		api.Query(d, connectionsapp.DescribeList(), application.APIShape{Area: "integrations", ID: "connections.schema", Response: reflect.TypeFor[connectionsapp.ConnectionsSchema]()},
			"GET /connections/schema", func(ctx context.Context, _ struct{}) (application.Read[connectionsapp.ConnectionsSchema], error) {
				s, err := connectionService(d)
				if err != nil {
					return application.Read[connectionsapp.ConnectionsSchema]{}, err
				}
				return s.Schema(ctx, connectionsapp.ListInput{})
			}),
	}
}

func revised[T any](body T) (application.Read[T], error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return application.Read[T]{}, err
	}
	sum := sha256.Sum256(raw)
	return application.Read[T]{Revision: application.Revision(hex.EncodeToString(sum[:12])), Data: body}, nil
}

func moduleStates(ctx context.Context) []ModuleState {
	out := []ModuleState{}
	set := module.SetFrom(ctx)
	if set == nil || set.Current() == nil {
		return out
	}
	for _, st := range set.Current().Statuses() {
		out = append(out, ModuleState{Name: st.Name, State: string(st.State), Reason: st.Reason()})
	}
	return out
}

func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	revision, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision != "" && dirty {
		revision += "+dirty"
	}
	return revision
}

func settingsSchema(ctx context.Context, svc *settingsapp.Service) (application.Read[SettingsSchema], error) {
	out := SettingsSchema{Pages: []SettingsSchemaPage{}}
	set := module.SetFrom(ctx)
	if set == nil {
		return application.Read[SettingsSchema]{Data: out}, nil
	}
	reg := settings.From(ctx)
	groups, sections, modules := set.SettingsLayout()
	type page struct {
		SettingsSchemaPage
		fields []settings.Field
	}
	var pages []page
	for _, g := range groups {
		fields, err := reg.Fields(g.Keys)
		if err != nil {
			return application.Read[SettingsSchema]{}, err
		}
		pages = append(pages, page{SettingsSchemaPage{Kind: "group", Module: g.Module, Name: g.Name, Title: g.Title, URL: g.URL}, fields})
	}
	for _, s := range sections {
		pages = append(pages, page{SettingsSchemaPage{Kind: "section", Module: s.Module, Name: s.Name, Title: s.Title, URL: "/settings/" + s.Module}, s.Fields})
	}
	for _, mod := range modules {
		fields, err := reg.Fields([]string{module.FlagKey(mod.Name)})
		if err != nil {
			return application.Read[SettingsSchema]{}, err
		}
		pages = append(pages, page{SettingsSchemaPage{Kind: "module", Module: mod.Name, Name: mod.Name, Title: mod.Name, URL: mod.URL}, fields})
	}
	var keys []string
	seen := map[string]bool{}
	for _, p := range pages {
		for _, f := range p.fields {
			if !seen[f.Key] {
				seen[f.Key] = true
				keys = append(keys, f.Key)
			}
		}
	}
	read, err := svc.Read(ctx, settingsapp.ReadInput{Keys: keys})
	if err != nil {
		return application.Read[SettingsSchema]{}, err
	}
	rows := make(map[string]settingsapp.Setting, len(read.Data.Rows))
	for _, row := range read.Data.Rows {
		rows[row.Key] = row
	}
	for _, p := range pages {
		p.Fields = []SettingsSchemaField{}
		for _, f := range p.fields {
			def, ok := reg.Lookup(f.Key)
			if !ok {
				continue
			}
			p.Fields = append(p.Fields, schemaField(def, f, rows[f.Key]))
		}
		out.Pages = append(out.Pages, p.SettingsSchemaPage)
	}
	return application.Read[SettingsSchema]{Revision: read.Revision, Data: out}, nil
}

func schemaField(def settings.Def, f settings.Field, row settingsapp.Setting) SettingsSchemaField {
	out := SettingsSchemaField{
		Key: def.Key, Label: f.Label, Module: def.Module, Type: string(def.Type), Multi: f.Multi, Choices: []SettingChoice{},
		Value: row.Value, Source: row.Source, PinSource: row.PinSource, Class: def.Class(), Secret: def.IsSecret(),
	}
	if out.Label == "" {
		out.Label = def.Key
	}
	if out.Type == "" {
		out.Type = string(settings.String)
	}
	for _, c := range f.Choices {
		label := f.ChoiceLabels[c]
		if label == "" {
			label = c
		}
		out.Choices = append(out.Choices, SettingChoice{Value: c, Label: label})
	}
	if def.Max > def.Min {
		lo, hi := def.Min, def.Max
		out.Min, out.Max = &lo, &hi
	}
	if !def.IsSecret() {
		value := def.Default
		out.Default = &value
	}
	pinned := row.Source != nil && *row.Source == string(settings.FromPin)
	out.Editable = def.Editable() && !pinned && !settings.HostOwnedKey(def.Key)
	return out
}
