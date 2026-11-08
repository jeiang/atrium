package pages

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestHistoryTabAndZoomRoutes(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.Text("hi")))
	thread := s.startThread("hello")
	s.exec("DELETE FROM memory_nodes WHERE thread_id = ?", thread)
	s.exec("DELETE FROM memory_views WHERE thread_id = ?", thread)
	s.exec("INSERT INTO memory_nodes (thread_id, level, idx, text, bytes, state, src_from_seq, src_to_seq, created_at) VALUES (?, 0, 0, 'first', 5, 'direct', 1, 1, 0), (?, 0, 1, 'second', 6, 'direct', 2, 2, 0), (?, 1, 0, 'summary one', 11, 'summary', 1, 2, 0)", thread, thread, thread)
	page := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, page, "panel-tab-history")
	hist := status(t, s.get(fmt.Sprintf("/chat/%d/history", thread)), http.StatusOK)
	mustContain(t, hist, "first", "second")
	kids := status(t, s.get(fmt.Sprintf("/chat/%d/history/1/2", thread)), http.StatusOK)
	mustContain(t, kids, "first", "second")
	leaf := status(t, s.get(fmt.Sprintf("/chat/%d/history/1/1", thread)), http.StatusOK)
	mustContain(t, leaf, "hello")
	status(t, s.get(fmt.Sprintf("/chat/%d/history/9/1", thread)), http.StatusNotFound)
	s.exec("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (3, 'chat', 'low', 0, 0)")
	s.exec(`INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (3, 1, 'user', '[{"type":"text","text":"only"}]', 0)`)
	s.exec("INSERT INTO memory_nodes (thread_id, level, idx, text, bytes, state, src_from_seq, src_to_seq, created_at) VALUES (3, 0, 0, 'only', 4, 'direct', 1, 1, 0)")
	page3 := status(t, s.get("/chat/3"), http.StatusOK)
	mustNotContain(t, page3, "panel-tab-history")
	status(t, s.get(fmt.Sprintf("/chat/%d/history/3/1", thread)), http.StatusNotFound)
}
