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

var hostDefs []HostDef

func MustRegisterHost(defs ...HostDef) {
	for _, d := range defs {
		if slices.ContainsFunc(hostDefs, func(e HostDef) bool { return e.Key == d.Key }) {
			panic(DuplicateKey(d.Key))
		}
		hostDefs = append(hostDefs, d)
	}
}

func HostDefs() []HostDef {
	out := make([]HostDef, 0, len(hostDefs)+len(importManifest))
	for _, d := range hostDefs {
		if ConnectionOwnedHostKey(d.Key) {
			continue
		}
		d.Choices = slices.Clone(d.Choices)
		out = append(out, d)
	}
	for _, e := range importManifest {
		if e.Class != ImportHost || e.Action == ImportRename || e.Module == "db" && e.Key == "key" || ConnectionOwnedHostKey(e.Key) {
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

func HostKey(key string) bool {
	return slices.ContainsFunc(HostDefs(), func(d HostDef) bool { return d.Key == key })
}

func DefaultOf(key string) string {
	entry, ok := ImportEntryFor(key)
	if !ok {
		return ""
	}
	return entry.Default
}

func HostDefOf(key string) (HostDef, bool) {
	if ConnectionOwnedHostKey(key) {
		return HostDef{}, false
	}
	for _, d := range hostDefs {
		if d.Key == key {
			return d, true
		}
	}
	return HostDef{}, false
}

func ValidateHost(key, value string) error {
	d, ok := HostDefOf(key)
	if !ok {
		return nil
	}
	return d.validate(value)
}

func (d HostDef) validate(value string) error {
	return validateTyped(d.Type, d.Choices, d.Check, value)
}
