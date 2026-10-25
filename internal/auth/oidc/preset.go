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
	Display     string
	Issuer      string
	Scopes      []string
	GroupsClaim string
	TrimSlash   bool
	DomainClaim string
	AuthParams  map[string]string

	IssuerLabel string
	Groups      bool
	Lists       bool
	Placeholder string
	Open        bool
	Steps       []string
}

const (
	StepRedirect = "{redirect}"
	StepAccess   = "{access}"
	StepAdmin    = "{admin}"
)

const copyCredentials = "Copy the client ID and secret into the form below."

var presets = []Preset{
	{
		Name: PresetGeneric, Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups",
		IssuerLabel: "Issuer URL", Groups: true, Open: true,
		Steps: []string{
			"Register a confidential client with your provider.",
			"Set the redirect URI to " + StepRedirect + ".",
			"Require PKCE with S256.",
			"Emit the groups " + StepAccess + " and " + StepAdmin + " in the groups claim.",
			copyCredentials,
		},
	},
	{
		Name: PresetPocketID, Display: "Pocket ID", Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups",
		IssuerLabel: "Issuer URL", Groups: true,
		Steps: []string{
			"Open OIDC Clients and choose Add client.",
			"Set the callback URL to " + StepRedirect + ".",
			"Enable PKCE.",
			"Allow the groups " + StepAccess + " and " + StepAdmin + ".",
			copyCredentials,
		},
	},
	{
		Name: PresetAuthelia, Display: "Authelia", Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups",
		IssuerLabel: "Issuer URL", Groups: true,
		Steps: []string{
			"Add a client under identity_providers.oidc.clients.",
			"Set redirect_uris to " + StepRedirect + ".",
			"Set pkce_challenge_method to S256.",
			"Grant the scopes openid, profile, email and groups.",
			"Put your users in the groups " + StepAccess + " and " + StepAdmin + ".",
			copyCredentials,
		},
	},
	{
		Name: PresetAuthentik, Display: "Authentik", Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups",
		IssuerLabel: "Issuer URL", Groups: true,
		Steps: []string{
			"Create an OAuth2/OpenID provider and an application for it.",
			"Set the redirect URI to " + StepRedirect + ".",
			"Use the provider's OpenID configuration issuer, ending in a slash.",
			"Put your users in the groups " + StepAccess + " and " + StepAdmin + ".",
			copyCredentials,
		},
	},
	{
		Name: PresetKeycloak, Display: "Keycloak", Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", TrimSlash: true,
		IssuerLabel: "Realm URL", Groups: true,
		Steps: []string{
			"Create a client with client authentication on.",
			"Set the valid redirect URI to " + StepRedirect + ".",
			"Set Proof Key for Code Exchange to S256.",
			"Add a Group Membership mapper with the token claim name groups.",
			"Put your users in the groups " + StepAccess + " and " + StepAdmin + ".",
			copyCredentials,
		},
	},
	{
		Name: PresetGoogle, Display: "Google", Issuer: "https://accounts.google.com", Scopes: []string{"openid", "email", "profile"}, DomainClaim: "hd", AuthParams: map[string]string{"access_type": "offline"},
		Lists: true,
		Steps: []string{
			"Open APIs & Services, Credentials and create an OAuth client ID of type Web application.",
			"Add the authorized redirect URI " + StepRedirect + ".",
			copyCredentials,
			"List the emails or domains that may sign in.",
		},
	},
	{
		Name: PresetEntra, Display: "Microsoft Entra", Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups",
		IssuerLabel: "Tenant ID", Groups: true, Lists: true, Placeholder: "Object ID",
		Steps: []string{
			"Register an application in Microsoft Entra ID.",
			"Add the Web redirect URI " + StepRedirect + ".",
			"Under Token configuration add the groups claim for group IDs.",
			"Create a client secret under Certificates & secrets.",
			copyCredentials,
			"Enter the object IDs of your access and admin groups.",
		},
	},
}

func (p Preset) Label() string {
	if p.Display == "" {
		return "Generic OIDC"
	}
	return p.Display
}

func Presets() []Preset { return slices.Clone(presets) }

func PresetNamed(name string) (Preset, bool) { return presetNamed(name) }

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
