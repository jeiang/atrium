package accounts

import "fmt"

// Method is how a mail account authenticates to its provider.
type Method string

const (
	Password Method = "password"
	XOAUTH2  Method = "xoauth2"
)

func (m Method) Valid() bool { return m == Password || m == XOAUTH2 }

// Parse reads a stored auth method; an empty value means the app password.
func Parse(value string) (Method, error) {
	m := Method(value)
	if value == "" {
		return Password, nil
	}
	if !m.Valid() {
		return "", fmt.Errorf("auth method must be %s or %s", Password, XOAUTH2)
	}
	return m, nil
}

// Allowed reports whether preset supports method. XOAUTH2 is Gmail-only.
func Allowed(preset string, m Method) bool {
	return m == Password || (m == XOAUTH2 && preset == "gmail")
}
