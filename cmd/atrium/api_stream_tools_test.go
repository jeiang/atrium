package main

import (
	"bufio"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func transcriptStep(t *testing.T, s *streamSite, threadID int64, kind string) map[string]any {
	t.Helper()
	read := s.do(http.MethodGet, fmt.Sprintf("/api/v1/threads/%d", threadID), s.token, nil)
	if read.status != http.StatusOK {
		t.Fatalf("transcript %d: %d %s", threadID, read.status, read.body)
	}
	items, _ := read.json["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		steps, _ := item["steps"].([]any)
		for _, rawStep := range steps {
			step, _ := rawStep.(map[string]any)
			if step["kind"] == kind && (kind != "tool" || step["name"] == "search_web") {
				return step
			}
		}
	}
	t.Fatalf("transcript %d has no %s step: %s", threadID, kind, read.body)
	return nil
}

func allFrames(t *testing.T, stream string) []streamFrame {
	t.Helper()
	var frames []streamFrame
	for chunk := range strings.SplitSeq(stream, "\n\n") {
		if parsed := parseFrames(t, chunk+"\n\n"); len(parsed) == 1 {
			frames = append(frames, parsed[0])
		} else if strings.Contains(chunk, "event: tool_progress\n") {
			parsed := parseFrames(t, "id: -1\n"+chunk+"\n\n")
			parsed[0].id = 0
			frames = append(frames, parsed[0])
		}
	}
	return frames
}

func TestStreamCarriesToolProgressAndSources(t *testing.T) {
	t.Parallel()
	s := newStreamSite(t)
	const a, b = "https://library.example/hours", "https://library.example/visit"
	s.api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{
			"query": "library hours", "question": "When does the library open?", "max_sources": 4,
		})),
		webSearchReply("library hours",
			anthropictest.SearchResult{URL: a, Title: "Opening <b>hours</b>"},
			anthropictest.SearchResult{URL: b}),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{
			"answer": "The library opens at nine [1]. Visit [2].",
			"citations": []any{
				map[string]any{"n": 1, "url": a, "quote": nil},
				map[string]any{"n": 2, "url": b, "quote": nil},
			},
		}})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
		anthropictest.Reply("end_turn", anthropictest.Text("It opens at nine.")),
	)
	free := s.hold()
	threadID, runID := s.start("when does the library open")
	resp := s.open(threadID, runID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	free()
	var live strings.Builder
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		live.WriteString(scan.Text() + "\n")
		if scan.Text() == "event: done" {
			break
		}
	}
	stream := live.String()
	frames := allFrames(t, stream)
	find := func(event, id string) (int, map[string]any) {
		for i, f := range frames {
			if f.kind == event && f.data["id"] == id {
				return i, f.data
			}
		}
		t.Fatalf("no %s for %s in:\n%s", event, id, stream)
		return 0, nil
	}
	answerAt := -1
	for i, f := range frames {
		if f.kind == "text" && f.run == runID {
			answerAt = i
			break
		}
	}
	if answerAt < 0 {
		t.Fatalf("no answer text in:\n%s", stream)
	}

	startAt, started := find("tool_start", "s1")
	progressAt, progress := find("tool_progress", "s1")
	if number(t, progress["run_id"]) != runID || progressAt < startAt {
		t.Errorf("tool_progress = %v at %d, want run %d after tool_start at %d", progress, progressAt, runID, startAt)
	}
	if text, _ := progress["text"].(string); text != `web_search {"query": "library hours"}` {
		t.Errorf("tool_progress text = %q, want the sub-agent's web_search", progress["text"])
	}
	for _, f := range frames {
		if f.kind == "tool_progress" && (f.data["id"] != "s1" || f.id != 0) {
			t.Errorf("unexpected tool_progress: %+v", f)
		}
	}
	replay, _ := s.events(s.token, threadID, runID, "")
	if strings.Contains(replay, "tool_progress") {
		t.Errorf("tool_progress was replayed:\n%s", replay)
	}
	if _, ok := started["results"]; ok {
		t.Errorf("tool_start carries results: %v", started)
	}

	searchAt, search := find("tool_done", "ws1")
	if searchAt > answerAt {
		t.Errorf("web_search sources at frame %d, after the answer at %d", searchAt, answerAt)
	}
	wantSearch := []any{
		map[string]any{"url": a, "title": "Opening <b>hours</b>"},
		map[string]any{"url": b, "title": b},
	}
	if !reflect.DeepEqual(search["results"], wantSearch) {
		t.Errorf("web_search results = %v, want %v", search["results"], wantSearch)
	}

	doneAt, done := find("tool_done", "s1")
	if doneAt > answerAt || done["ok"] != true {
		t.Errorf("search_web tool_done = %v at %d, answer at %d", done, doneAt, answerAt)
	}
	step := transcriptStep(t, s, threadID, "tool")
	if len(step["results"].([]any)) != 2 || !reflect.DeepEqual(done["results"], step["results"]) {
		t.Errorf("search_web results = %v, transcript has %v", done["results"], step["results"])
	}
	child := transcriptStep(t, s, number(t, step["child_thread_id"]), "web_search")
	if !reflect.DeepEqual(search["results"], child["results"]) {
		t.Errorf("web_search results = %v, child transcript has %v", search["results"], child["results"])
	}
}
