package registrytest_test

import (
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
)

func TestStubsVerify(t *testing.T) {
	stubs := registrytest.Load(t).Stubs(t)
	if len(stubs) != 59 {
		t.Fatalf("%d stubs, want 59", len(stubs))
	}
	for _, s := range stubs {
		registrytest.Verify(t, s)
	}
}

func TestMatrixIsSortedAndComplete(t *testing.T) {
	rows := registrytest.Matrix(t)
	if !slices.IsSortedFunc(rows, func(a, b registrytest.Entry) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	}) {
		t.Error("matrix.json is not sorted by name")
	}
	for _, r := range rows {
		if len(r.Contexts) == 0 || !r.Effect.Valid() {
			t.Errorf("%s: bad row %+v", r.Name, r)
		}
	}
}
