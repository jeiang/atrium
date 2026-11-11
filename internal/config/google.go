package config

import "strings"

const (
	GoogleAuthURLDefault  = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURLDefault = "https://oauth2.googleapis.com/token"
	GoogleAPIURLDefault   = "https://www.googleapis.com/calendar/v3"
)

func GoogleAuthURL() string { return envOr("ATRIUM_GOOGLE_AUTH_URL", GoogleAuthURLDefault) }

func GoogleTokenURL() string { return envOr("ATRIUM_GOOGLE_TOKEN_URL", GoogleTokenURLDefault) }

func GoogleAPIURL() string { return envOr("ATRIUM_GOOGLE_API_URL", GoogleAPIURLDefault) }

func envOr(name, fallback string) string {
	if v, ok := OS(name); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return fallback
}
