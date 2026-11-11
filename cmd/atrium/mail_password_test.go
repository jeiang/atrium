package main

import (
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/mail/mailtest"
)

func TestMailConnectionTestNormalizesAGroupedAppPassword(t *testing.T) {
	dir := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir, mailAccount{"home", "icloud", "me@home.test", "aaaa bbbb cccc dddd", false}))
	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	strictIMAPRig(t, map[string]string{"me@home.test": "aaaabbbbccccdddd"}, map[string]*mailtest.FakeClient{"home": fake})
	strictSMTPRig(t, map[string]string{"me@home.test": "aaaabbbbccccdddd"})

	ran := runApp(t, "connections", "test", "mail/home")
	if ran.code != 0 || !strings.Contains(ran.stdout, "mail/home\tok") {
		t.Fatalf("testing a grouped app password: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
}
