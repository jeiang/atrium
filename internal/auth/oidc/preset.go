package oidc

import "slices"

const (
	PresetGeneric   = "generic"
	PresetPocketID  = "pocket_id"
	PresetAuthelia  = "authelia"
	PresetAuthentik = "authentik"
	PresetKeycloak  = "keycloak"
	PresetGoogle    = "google"
	PresetEntra     = "entra"
)

type Preset struct {
	Name        string
	Issuer      string
	Scopes      []string
	GroupsClaim string
	TrimSlash   bool
	AuthParams  map[string]string
}

var presets = []Preset{
	{Name: PresetGeneric, Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups"},
	{Name: PresetPocketID, Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups"},
	{Name: PresetAuthelia, Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups"},
	{Name: PresetAuthentik, Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups"},
	{Name: PresetKeycloak, Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", TrimSlash: true},
	{Name: PresetGoogle, Issuer: "https://accounts.google.com", Scopes: []string{"openid", "email", "profile"}, AuthParams: map[string]string{"access_type": "offline"}},
	{Name: PresetEntra, Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups"},
}

func PresetNames() []string {
	names := make([]string, len(presets))
	for i, p := range presets {
		names[i] = p.Name
	}
	return names
}

func presetNamed(name string) (Preset, bool) {
	i := slices.IndexFunc(presets, func(p Preset) bool { return p.Name == name })
	if i < 0 {
		return Preset{}, false
	}
	return presets[i], true
}
