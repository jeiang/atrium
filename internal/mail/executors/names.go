package executors

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/proposals"
	"golang.org/x/text/cases"
)

const MaxNameChars = 200

func ValidName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" || utf8.RuneCountInString(name) > MaxNameChars || strings.Contains(name, "|") ||
		cases.Fold().String(name) == cases.Fold().String(config.Inbox) || strings.ContainsFunc(name, func(r rune) bool { return r < 32 }) {
		return "", proposals.SelectionError("invalid folder name " + strconv.Quote(value))
	}
	return name, nil
}
