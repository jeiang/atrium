package mail

import (
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
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

// IsSystemFlag is flag.lower() in sync.SYSTEM_FLAGS.
func IsSystemFlag(flag string) bool {
	_, ok := systemFlags[py.Lower(flag)]
	return ok
}

// IsSystemName is name.casefold() in sync.SYSTEM_NAMES.
func IsSystemName(name string) bool {
	folded := py.Casefold(name)
	for _, n := range systemNames {
		if folded == n[0] {
			return true
		}
	}
	return false
}

// MailboxRole is sync.mailbox_role: Gmail has only its All Mail and system labels; an iCloud mailbox is a
// system mailbox by its special-use flag or name, the Inbox, Misc, or a filed folder.
func MailboxRole(name string, flags []string, account string) string {
	if account == config.Gmail {
		if name == config.GmailAllMail {
			return "allmail"
		}
		return "system"
	}
	for _, flag := range flags {
		if role, ok := systemFlags[py.Lower(flag)]; ok {
			return role
		}
	}
	folded := py.Casefold(name)
	if folded == py.Casefold(config.Inbox) {
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
