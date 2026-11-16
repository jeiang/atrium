package oidc

import (
	"slices"
	"strconv"
	"strings"
)

const (
	PresetGeneric  = "generic"
	PresetPocketID = "pocket_id"
)

type Preset struct {
	Name        string
	Display     string
	Scopes      []string
	GroupsClaim string
}

var presets = []Preset{
	{Name: PresetGeneric, Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups"},
	{Name: PresetPocketID, Display: "Pocket ID", Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups"},
}

func presetNamed(name string) (Preset, bool) {
	i := slices.IndexFunc(presets, func(p Preset) bool { return p.Name == name })
	if i < 0 {
		return Preset{}, false
	}
	return presets[i], true
}

func unknownPreset(name string) string {
	names := make([]string, len(presets))
	for i, p := range presets {
		names[i] = p.Name
	}
	return "unknown preset " + strconv.Quote(name) + "; use " + strings.Join(names, " or ")
}
