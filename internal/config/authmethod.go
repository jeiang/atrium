package config

import "fmt"

// AuthMethod is how a mail account authenticates to its provider.
type AuthMethod string

const (
	AuthPassword AuthMethod = "password"
	AuthXOAUTH2  AuthMethod = "xoauth2"
)

func (m AuthMethod) Valid() bool { return m == AuthPassword || m == AuthXOAUTH2 }

// ParseAuthMethod reads a stored auth method; an empty value means the app password.
func ParseAuthMethod(value string) (AuthMethod, error) {
	m := AuthMethod(value)
	if value == "" {
		return AuthPassword, nil
	}
	if !m.Valid() {
		return "", fmt.Errorf("auth method must be %s or %s", AuthPassword, AuthXOAUTH2)
	}
	return m, nil
}

// AuthAllowed reports whether preset supports method. XOAUTH2 is Gmail-only.
func AuthAllowed(preset string, m AuthMethod) bool {
	return m == AuthPassword || (m == AuthXOAUTH2 && preset == "gmail")
}
