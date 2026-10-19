package executors

import (
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

// MaxNameChars is MAX_NAME_CHARS.
const MaxNameChars = 200

// ValidName is valid_name: the stripped folder or label name, or a SelectionError.
func ValidName(value string) (string, error) {
	name := py.Strip(value)
	if name == "" || utf8.RuneCountInString(name) > MaxNameChars || strings.Contains(name, "|") ||
		py.Casefold(name) == py.Casefold(config.Inbox) || strings.ContainsFunc(name, func(r rune) bool { return r < 32 }) {
		return "", proposals.SelectionError("invalid folder name " + py.Repr(value))
	}
	return name, nil
}
