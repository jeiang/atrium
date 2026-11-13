package settings

import (
	"slices"
	"strings"
)

type HostDef struct {
	Key      string
	Module   string
	Type     Type
	Default  string
	Choices  []string
	Secret   bool
	EnvAlias string
	Check    func(value string) error
}

func (r *Registry) HostDefs() []HostDef { return HostDefsFrom(r.host) }

func HostDefsFrom(defs []HostDef) []HostDef {
	out := make([]HostDef, 0, len(defs)+len(importManifest))
	for _, d := range defs {
		if ConnectionOwnedHostKey(d.Key) {
			continue
		}
		d.Choices = slices.Clone(d.Choices)
		out = append(out, d)
	}
	for _, e := range importManifest {
		if e.Class != ImportHost || e.Retired || e.Action == ImportRename || e.Module == "db" && e.Key == "key" || ConnectionOwnedHostKey(e.Key) {
			continue
		}
		if slices.ContainsFunc(out, func(d HostDef) bool { return d.Key == e.Key }) {
			continue
		}
		out = append(out, HostDef{Key: e.Key, Module: e.Module, Type: e.Type, Default: e.Default, Choices: slices.Clone(e.Choices), Secret: e.Secret})
	}
	slices.SortFunc(out, func(a, b HostDef) int { return strings.Compare(a.Key, b.Key) })
	return out
}

func (r *Registry) HostKey(key string) bool {
	return slices.ContainsFunc(r.HostDefs(), func(d HostDef) bool { return d.Key == key })
}

func DefaultOf(key string) string {
	entry, ok := ImportEntryFor(key)
	if !ok {
		return ""
	}
	return entry.Default
}

func (r *Registry) HostDefOf(key string) (HostDef, bool) {
	if ConnectionOwnedHostKey(key) {
		return HostDef{}, false
	}
	for _, d := range r.host {
		if d.Key == key {
			return d, true
		}
	}
	return HostDef{}, false
}

func (r *Registry) ValidateHost(key, value string) error {
	d, ok := r.HostDefOf(key)
	if !ok {
		return nil
	}
	return d.validate(value)
}

func (d HostDef) validate(value string) error {
	return validateTyped(d.Type, d.Choices, d.Check, value)
}
