package sync

import (
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/py"
)

var systemRoles = map[string]bool{"trash": true, "sent": true, "drafts": true, "junk": true}

var indexedSystemRoles = map[string]bool{"sent": true}

// State is message_state: the columns a message's flags and labels determine.
type State struct {
	Seen, Flagged, InInbox, Sent int
	Location                     string
	Folder                       *string
	Flags, Labels                string
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// MessageState is sync.message_state. A nil labels is Python's None.
func MessageState(account, role, mailboxName string, flags, labels []string) State {
	var st State
	st.Seen = bit(slices.Contains(flags, `\Seen`))
	st.Flagged = bit(slices.Contains(flags, `\Flagged`))
	if account == config.Gmail {
		if labels == nil {
			labels = []string{}
		}
		leaves := mail.CollapseLabels(labels)
		inInbox := slices.Contains(labels, `\Inbox`)
		st.InInbox = bit(inInbox)
		st.Sent = bit(slices.Contains(labels, `\Sent`) || slices.Contains(labels, `\Draft`) || slices.Contains(flags, `\Draft`))
		st.Location = strings.Join(leaves, "|")
		if st.Location == "" {
			st.Location = "(none)"
			if inInbox {
				st.Location = config.Inbox
			}
		}
		if len(leaves) == 1 {
			st.Folder = &leaves[0]
		}
	} else {
		labels = []string{}
		st.InInbox = bit(role == "inbox")
		st.Sent = bit(role == "sent")
		st.Location = mailboxName
		if role == "filed" {
			st.Folder = &mailboxName
		}
	}
	sorted := slices.Clone(flags)
	slices.Sort(sorted)
	st.Flags = py.JSONDumps(append([]string{}, sorted...))
	st.Labels = py.JSONDumps(labels)
	return st
}
