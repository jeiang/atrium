package module

import (
	"errors"

	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/runtime"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settingspage"
)

func Compile(decls []Declaration) (*Catalog, error) {
	byName := make(map[string]Declaration, len(decls))
	for _, d := range decls {
		if strings.TrimSpace(d.Name) == "" {
			return nil, &InvalidDeclaration{Module: d.Name, Field: "name"}
		}
		if _, ok := byName[d.Name]; ok {
			return nil, DuplicateModule(d.Name)
		}
		byName[d.Name] = d
	}
	core := ""
	for _, d := range decls {
		if !d.Core {
			continue
		}
		if core != "" {
			return nil, &InvalidCore{Reason: "exactly one module must be core, found " + core + " and " + d.Name}
		}
		core = d.Name
	}
	switch {
	case core == "":
		return nil, &InvalidCore{Reason: "exactly one module must be core"}
	case core != Core:
		return nil, &InvalidCore{Module: core, Reason: "must be named " + Core}
	}
	for _, d := range decls {
		if d.Core && (len(d.Needs) > 0 || len(d.Requires) > 0) {
			return nil, &InvalidCore{Module: d.Name}
		}
		for _, dep := range d.Needs {
			if _, ok := byName[dep]; !ok {
				return nil, &UnknownDependency{Module: d.Name, Dependency: dep}
			}
		}
	}
	if _, err := byNeeds(decls); err != nil {
		return nil, err
	}
	jobs, err := compileJobs(decls, byName)
	if err != nil {
		return nil, err
	}
	services, err := compileServices(decls)
	if err != nil {
		return nil, err
	}
	reg, err := compileSettings(decls, jobs)
	if err != nil {
		return nil, err
	}
	kinds := map[string]bool{}
	for _, d := range decls {
		for _, k := range d.ProposeKinds {
			if strings.TrimSpace(k.Kind.Name) == "" || k.Build == nil || k.Build.Build == nil {
				return nil, &InvalidDeclaration{Module: d.Name, Field: "propose kinds"}
			}
			if kinds[k.Kind.Name] {
				return nil, proposals.DuplicateKind(k.Kind.Name)
			}
			kinds[k.Kind.Name] = true
		}
	}
	commands := map[string]bool{}
	sectionNames := map[string]bool{}
	var sections []settingspage.Section
	for _, d := range decls {
		for _, section := range d.Sections {
			if strings.TrimSpace(section.Name) == "" || sectionNames[section.Name] {
				return nil, &DeclarationError{Module: d.Name, Field: "sections", Err: settingspage.DuplicateSection(section.Name)}
			}
			sectionNames[section.Name] = true
			sections = append(sections, section)
		}
	}
	for _, d := range decls {
		for _, rq := range d.Requires {
			if rq.Key == "" || rq.Host || rq.Connection {
				continue
			}
			if _, ok := reg.Lookup(rq.Key); !ok {
				return nil, settings.UnknownKey(rq.Key)
			}
		}
		groups := map[string]bool{}
		for _, g := range d.Groups {
			if g.Name == "" || g.Title == "" || groups[g.Name] {
				return nil, &InvalidGroup{Module: d.Name, Group: g.Name}
			}
			groups[g.Name] = true
			for _, key := range g.Keys {
				def, ok := reg.Lookup(key)
				if !ok {
					return nil, settings.UnknownKey(key)
				}
				if !def.Editable() {
					return nil, &InvalidGroup{Module: d.Name, Group: g.Name}
				}
			}
		}
		for _, c := range d.Commands {
			if commands[c.Name] {
				return nil, DuplicateCommand(c.Name)
			}
			commands[c.Name] = true
		}
		for _, p := range d.Pages {
			if p.Module == nil && len(p.Globals) == 0 && p.Templates == nil && len(p.Nav) == 0 {
				return nil, &InvalidPage{Module: d.Name}
			}
		}
	}
	for _, d := range decls {
		for path, leaf := range d.Leaves {
			if !hasPath(d.Commands, strings.Fields(path)) {
				return nil, &UnknownLeaf{Module: d.Name, Path: path}
			}
			if leaf.Always && len(leaf.Needs) > 0 {
				return nil, &InvalidLeaf{Module: d.Name, Path: path}
			}
			for _, name := range leaf.Needs {
				if _, ok := byName[name]; !ok {
					return nil, &UnknownDependency{Module: d.Name, Dependency: name}
				}
			}
		}
	}
	metrics := map[string]bool{}
	checks := map[string]bool{}
	var allMetrics []ops.Metric
	var allChecks []ops.Check
	tools := map[string]bool{}
	scratch := registry.New(settings.NewFrom(nil))
	var optional []string
	for _, d := range decls {
		if !d.Core {
			optional = append(optional, d.Name)
		}
		for _, m := range d.Metrics {
			if metrics[m.Name] {
				return nil, &DuplicateMetric{Name: m.Name}
			}
			metrics[m.Name] = true
			allMetrics = append(allMetrics, m)
		}
		for _, c := range d.Checks {
			if checks[c.Name] {
				return nil, &DuplicateCheck{Name: c.Name}
			}
			checks[c.Name] = true
			allChecks = append(allChecks, c)
		}
		for _, def := range d.Tools {
			if tools[def.Tool.Name] {
				return nil, &DeclarationError{Module: d.Name, Field: "tools", Err: DuplicateTool(def.Tool.Name)}
			}
			tools[def.Tool.Name] = true
			if _, ok := reg.Lookup("tool_" + def.Tool.Name + "_enabled"); ok {
				return nil, &ToolSettingCollision{Module: d.Name, Key: "tool_" + def.Tool.Name + "_enabled"}
			}
			var err error
			if def.Tool.Handler == nil && !def.Tool.Server {
				err = scratch.Declare(def.Tool)
			} else {
				err = scratch.Add(def.Tool)
			}
			if err != nil {
				return nil, &DeclarationError{Module: d.Name, Field: "tools", Err: err}
			}
		}
	}
	conns, hooks, credential, err := compileConnections(decls)
	if err != nil {
		return nil, err
	}
	return &Catalog{decls: slices.Clone(decls), optional: optional, settings: reg, sections: sections,
		conns: conns, hooks: hooks, credential: credential, metrics: allMetrics, checks: allChecks, jobs: jobs, services: services}, nil
}

func compileJobs(decls []Declaration, byName map[string]Declaration) ([]scheduler.Definition, error) {
	var jobs []scheduler.Definition
	for _, d := range decls {
		for _, job := range d.Jobs {
			if job.Module != d.Name {
				return nil, &DeclarationError{Module: d.Name, Field: "jobs", Err: &scheduler.InvalidJob{Key: job.Key, Reason: "is owned by " + job.Module}}
			}
			for _, dep := range job.Needs {
				if _, ok := byName[dep]; !ok {
					return nil, &UnknownDependency{Module: d.Name, Dependency: dep}
				}
			}
			jobs = append(jobs, job)
		}
	}
	compiled, err := scheduler.Compile(jobs)
	if err != nil {
		var bad *scheduler.InvalidJob
		if errors.As(err, &bad) {
			return nil, &DeclarationError{Module: bad.Module, Field: "jobs", Err: err}
		}
		return nil, err
	}
	return compiled, nil
}

func compileConnections(decls []Declaration) (*connections.Registry, proposals.Hooks, config.AccountCredentialFunc, error) {
	var (
		defs        []connections.Definition
		sync        *connections.AccountSync
		notice      connections.Notifier
		credential  config.AccountCredentialFunc
		created     proposals.CreatedHook
		syncOwner   string
		noticeOwner string
		credOwner   string
		createdBy   string
	)
	for _, d := range decls {
		defs = append(defs, d.Connections...)
		if d.AccountSync != nil {
			if syncOwner != "" {
				return nil, proposals.Hooks{}, nil, &DuplicateHook{Hook: "account sync", Modules: []string{syncOwner, d.Name}}
			}
			sync, syncOwner = d.AccountSync, d.Name
		}
		if d.ConnectionNotice != nil {
			if noticeOwner != "" {
				return nil, proposals.Hooks{}, nil, &DuplicateHook{Hook: "connection notice", Modules: []string{noticeOwner, d.Name}}
			}
			notice, noticeOwner = d.ConnectionNotice, d.Name
		}
		if d.AccountCredential != nil {
			if credOwner != "" {
				return nil, proposals.Hooks{}, nil, &DuplicateHook{Hook: "account credential", Modules: []string{credOwner, d.Name}}
			}
			credential, credOwner = d.AccountCredential, d.Name
		}
		if d.ProposalCreated != nil {
			if createdBy != "" {
				return nil, proposals.Hooks{}, nil, &DuplicateHook{Hook: "proposal created", Modules: []string{createdBy, d.Name}}
			}
			created, createdBy = d.ProposalCreated, d.Name
		}
	}
	reg, err := connections.Compile(defs, sync, notice)
	if err != nil {
		return nil, proposals.Hooks{}, nil, err
	}
	return reg, proposals.Hooks{Created: created}, credential, nil
}

func compileSettings(decls []Declaration, jobs []scheduler.Definition) (*settings.Registry, error) {
	defs := make([]settings.Def, 0, len(decls))
	hosts := make([]settings.HostDef, 0, len(decls))
	owner := make(map[string]string, len(decls))
	for _, d := range decls {
		if _, err := settings.Compile(d.Settings, d.Host); err != nil {
			return nil, &DeclarationError{Module: d.Name, Field: "settings", Err: err}
		}
		for _, def := range d.Settings {
			owner[def.Key] = d.Name
		}
		for _, def := range d.Host {
			owner[def.Key] = d.Name
		}
		defs = append(defs, d.Settings...)
		hosts = append(hosts, d.Host...)
		if d.Core {
			continue
		}
		defs = append(defs, FlagDef(d.Name))
		hosts = append(hosts, FlagHostDef(d.Name))
	}
	if len(jobs) > 0 {
		for _, def := range scheduler.Settings(jobs) {
			owner[def.Key] = def.Module
			defs = append(defs, def)
		}
	}
	reg, err := settings.Compile(defs, hosts)
	if err != nil {
		var dup settings.DuplicateKey
		if errors.As(err, &dup) {
			return nil, &DeclarationError{Module: owner[string(dup)], Field: "settings", Err: err}
		}
		return nil, err
	}
	return reg, nil
}

func hasPath(cmds []*cli.Command, path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, c := range cmds {
		if c.Name != path[0] {
			continue
		}
		if len(path) == 1 {
			return true
		}
		return hasPath(c.Commands, path[1:])
	}
	return false
}

func compileServices(decls []Declaration) ([]runtime.ServiceDefinition, error) {
	var all []runtime.ServiceDefinition
	owners := map[string]string{}
	for _, d := range decls {
		for _, def := range d.Services {
			if prev, ok := owners[def.Name]; ok {
				return nil, &DeclarationError{Module: d.Name, Field: "services", Err: errors.New("service " + def.Name + " is also declared by " + prev)}
			}
			owners[def.Name] = d.Name
			if err := runtime.Validate([]runtime.ServiceDefinition{def}); err != nil {
				return nil, &DeclarationError{Module: d.Name, Field: "services", Err: err}
			}
			all = append(all, def)
		}
	}
	return all, nil
}
