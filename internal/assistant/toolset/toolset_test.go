package toolset_test

import (
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/toolset"
)

func TestEveryRegisteredToolIsThePythonTool(t *testing.T) {
	reg, err := toolset.Registry()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := reg.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notify", "web_fetch", "commitments", "contacts_lookup", "draft_email_create", "draft_email_update",
		"mail_read", "mail_search", "open_loops", "packages", "upcoming_birthdays"} {
		found := false
		for _, tl := range tools {
			found = found || tl.Name == name
		}
		if !found {
			t.Errorf("%s is not registered", name)
		}
	}
	for _, tl := range tools {
		registrytest.Verify(t, tl)
	}
}
