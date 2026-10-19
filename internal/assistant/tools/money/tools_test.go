package moneytools_test

import (
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	moneytools "github.com/jeiang/atrium/internal/assistant/tools/money"
)

var wantNames = []string{
	"bills", "payee_alias_add", "payee_merge", "payee_rename", "payee_set_category", "payees", "spending",
	"transaction_merge", "transaction_set_account", "transaction_split", "transactions",
}

func TestToolsArePythonTools(t *testing.T) {
	var names []string
	for _, tl := range moneytools.Tools() {
		names = append(names, tl.Name)
		registrytest.Verify(t, tl)
	}
	slices.Sort(names)
	if !slices.Equal(names, wantNames) {
		t.Errorf("tools %v, want %v", names, wantNames)
	}
}
