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
		"mail_read", "mail_search", "open_loops", "packages", "upcoming_birthdays", "threads_today",
		"context_status", "context_pin", "context_drop", "context_fold", "recall", "plan", "profile_get", "profile_set", "propose",
		"schedule_create", "schedule_update", "schedule_cancel", "schedule_list", "load_skill",
		"search_knowledge", "fetch_knowledge", "notion_create_page", "notion_update_page", "notion_move_page", "notion_archive_page", "notion_add_alias",
		"grafana_datasources", "grafana_query", "grafana_alerts", "grafana_search_dashboards",
		"spending", "transactions", "bills", "payees", "payee_rename", "payee_merge", "payee_alias_add", "payee_set_category",
		"transaction_merge", "transaction_split", "transaction_set_account"} {
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
