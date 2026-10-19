package handlers_test

import (
	"testing"

	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
)

func TestRegisterAddsTheMailAndReminderKinds(t *testing.T) {
	r := proposals.NewRegistry()
	handlers.Register(r, handlers.Deps{DB: dbtest.Schema(t)})
	for _, kind := range []string{"rebalance_split", "rebalance_merge", "deletion_rule", "gmail_label_merge", "inbox_backlog", "reminder"} {
		if r.Executors[kind] == nil {
			t.Errorf("no executor for %s", kind)
		}
	}
	for _, op := range []string{"delete_folder", "rule_delete", "reminder_cancel"} {
		if r.Undoers[op] == nil {
			t.Errorf("no undoer for %s", op)
		}
	}
}
