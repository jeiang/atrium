package settings

import "slices"

type HostDef struct {
	Key      string
	Module   string
	Type     Type
	Default  string
	Choices  []string
	EnvAlias string
}

func HostDefs() []HostDef {
	var out []HostDef
	for _, e := range importManifest {
		if e.Class != ImportHost {
			continue
		}
		out = append(out, HostDef{Key: e.Key, Module: e.Module, Type: e.Type, Default: e.Default, Choices: slices.Clone(e.Choices)})
	}
	return out
}
