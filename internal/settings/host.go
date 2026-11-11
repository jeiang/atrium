package settings

import "slices"

type HostDef struct {
	Key      string
	Module   string
	Type     Type
	Default  string
	Choices  []string
	Secret   bool
	EnvAlias string
}

func HostDefs() []HostDef {
	var out []HostDef
	for _, e := range importManifest {
		if e.Class != ImportHost || e.Action == ImportRename || e.Module == "db" && e.Key == "key" {
			continue
		}
		def, ok := LookupOf(e.Key)
		out = append(out, HostDef{
			Key: e.Key, Module: e.Module, Type: e.Type, Default: e.Default,
			Choices: slices.Clone(e.Choices), Secret: ok && def.IsSecret(),
		})
	}
	return out
}

func ValidateHost(key, value string) error {
	def, ok := LookupOf(key)
	if !ok {
		return nil
	}
	return def.validate(value)
}
