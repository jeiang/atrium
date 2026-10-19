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
	for _, name := range []string{"notify", "web_fetch"} {
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
