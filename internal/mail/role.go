package mail

import (
	"strings"

	"golang.org/x/text/cases"

	"github.com/jeiang/atrium/internal/config"
)

var systemFlags = map[string]string{`\trash`: "trash", `\sent`: "sent", `\drafts`: "drafts", `\junk`: "junk"}

var systemNames = [...][2]string{
	{"trash", "trash"},
	{"deleted messages", "trash"},
	{"deleted items", "trash"},
	{"bin", "trash"},
	{"sent", "sent"},
	{"sent messages", "sent"},
	{"sent items", "sent"},
	{"drafts", "drafts"},
	{"junk", "junk"},
	{"spam", "junk"},
}

func IsSystemFlag(flag string) bool {
	_, ok := systemFlags[strings.ToLower(flag)]
	return ok
}

func IsSystemName(name string) bool {
	folded := cases.Fold().String(name)
	for _, n := range systemNames {
		if folded == n[0] {
			return true
		}
	}
	return false
}

func MailboxRole(name string, flags []string, preset string) string {
	if preset == config.Gmail {
		if name == config.GmailAllMail {
			return "allmail"
		}
		return "system"
	}
	for _, flag := range flags {
		if role, ok := systemFlags[strings.ToLower(flag)]; ok {
			return role
		}
	}
	folded := cases.Fold().String(name)
	if folded == cases.Fold().String(config.Inbox) {
		return "inbox"
	}
	if folded == "misc" {
		return "misc"
	}
	for _, n := range systemNames {
		if folded == n[0] {
			return n[1]
		}
	}
	return "filed"
}
