package mail

import (
	"strings"

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
		if role, ok := systemFlags[strings.ToLower(flag)]; ok {
			return role
		}
	}
	if strings.EqualFold(name, config.Inbox) {
		return "inbox"
	}
	if strings.EqualFold(name, "misc") {
		return "misc"
	}
	for _, n := range systemNames {
		if strings.EqualFold(name, n[0]) {
			return n[1]
		}
	}
	return "filed"
}
