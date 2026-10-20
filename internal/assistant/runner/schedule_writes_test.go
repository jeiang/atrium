package runner_test

import (
	"testing"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/threads"
)

func TestScheduleToolsNeverRecordAgentWrites(t *testing.T) {
	w := newWorld(t)
	if err := scheduler.EnsureNightly(t.Context(), w.d.Write, tz, noon); err != nil {
		t.Fatal(err)
	}
	w.turn(w.thread(threads.Chat), threads.Chat, "schedule",
		toolTurn(use("c1", "schedule_create", map[string]any{"name": "weekly", "prompt": "Review.", "schedule": "weekly fri 16:00"})),
		toolTurn(use("u1", "schedule_update", map[string]any{"id": 2, "schedule": "daily 08:00"})),
		toolTurn(use("x1", "schedule_cancel", map[string]any{"id": 2})),
		reply("done"),
	)
	if got := decoded(t, resultOf(t, w.request(2), "u1")); got["schedule"] != "daily 08:00" {
		t.Fatalf("update %v", got)
	}
	var n int
	if err := w.d.Read.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM agent_writes").Scan(&n); err != nil || n != 0 {
		t.Errorf("agent_writes = %d, %v", n, err)
	}
}
