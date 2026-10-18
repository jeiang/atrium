package mail

import "testing"

func TestMailboxRole(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		account string
		want    string
	}{
		{"INBOX", nil, "icloud", "inbox"},
		{"inbox", nil, "icloud", "inbox"},
		{"Misc", nil, "icloud", "misc"},
		{"Bills/Utilities", nil, "icloud", "filed"},
		{"Whatever", []string{`\Trash`}, "icloud", "trash"},
		{"Whatever", []string{`\SENT`}, "icloud", "sent"},
		{"Junk", nil, "icloud", "junk"},
		{"Spam", nil, "icloud", "junk"},
		{"Sent Meßages", nil, "icloud", "sent"},
		{"Deleted Meßages", nil, "icloud", "trash"},
		{"DELETED ITEMS", nil, "icloud", "trash"},
		{"Bin", nil, "icloud", "trash"},
		{"[Gmail]/All Mail", nil, "gmail", "allmail"},
		{"INBOX", nil, "gmail", "system"},
	} {
		if got := MailboxRole(tc.name, tc.flags, tc.account); got != tc.want {
			t.Errorf("MailboxRole(%q, %v, %q) = %q, want %q", tc.name, tc.flags, tc.account, got, tc.want)
		}
	}
}
