package research_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const runCodeSummary = " For several read-only lookups across sources, call run_code once instead of many tool turns."

func canonical(t *testing.T, raw []byte, recorded bool) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if system, ok := body["system"].([]any); ok && recorded {
		first := system[0].(map[string]any)
		text := first["text"].(string)
		if strings.Count(text, runCodeSummary) != 1 {
			t.Fatal("the recorded system prompt lost the run_code sentence: regenerate and revisit prompts.System")
		}
		first["text"] = strings.Replace(text, runCodeSummary, "", 1)
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSubagentRequestGolden replays subagent_research through the real research tool: the parent's research
// call starts a web child whose requests, system addendum, allowlist and 5-minute cache TTL, the rows it
// leaves and the digest the parent receives all equal what the frozen Python app recorded.
func TestSubagentRequestGolden(t *testing.T) {
	s := golden.LoadScenario(t, "anthropic", "subagent_research")
	stab := registrytest.Load(t)
	w := newWorld(t, nil)
	w.runner.Profile = func(_ context.Context, _ db.Querier) (string, error) { return stab.SystemTexts["profile"], nil }
	for _, ex := range s.Exchanges {
		resp, err := anthropictest.FromGolden(ex.Response)
		if err != nil {
			t.Fatal(err)
		}
		w.srv.Push(resp)
	}
	w.svc.Now = clockFrom(wallClock(2026, time.October, 15, 12, 0))
	thread, err := service.CreateThread(t.Context(), w.d.Write, w.svc.Now(), threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RunOnce(t.Context(), thread, "Ask the web agent when carnival 2027 is.", service.Start{Kind: threads.Chat, Operator: true}); err != nil {
		t.Fatal(err)
	}
	got := w.srv.Requests()
	if len(got) != len(s.Exchanges) {
		t.Fatalf("%d requests, recorded %d", len(got), len(s.Exchanges))
	}
	for i, ex := range s.Exchanges {
		golden.EqualJSON(t, canonical(t, ex.Request.Body, true), canonical(t, got[i].Body, false))
	}
	web, err := subagents.Get(t.Context(), w.d.Read, "web")
	if err != nil {
		t.Fatal(err)
	}
	if extra := subagents.SystemAddendum(*web); extra != stab.SystemTexts["role_addendum:web"] {
		t.Errorf("addendum differs from the recording:\n%s", extra)
	}
	rows, err := w.d.Read.Query("SELECT id, thread_id, parent_run_id, kind, effort, state, error, steps FROM runs ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var runs []any
	for rows.Next() {
		var id, thr, steps int64
		var parent *int64
		var kind, effort, state string
		var errText *string
		if err := rows.Scan(&id, &thr, &parent, &kind, &effort, &state, &errText, &steps); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, map[string]any{"id": id, "thread_id": thr, "parent_run_id": parent, "kind": kind, "effort": effort, "state": state, "error": errText, "steps": steps})
	}
	_ = rows.Close()
	want, _ := json.Marshal(s.Runs)
	have, _ := json.Marshal(runs)
	golden.EqualJSON(t, want, have)
	if n := w.srv.Remaining(); n != 0 {
		t.Errorf("%d recorded responses unused", n)
	}
}

func wallClock(year int, month time.Month, day, hour, minute int) float64 {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		panic(err)
	}
	return float64(time.Date(year, month, day, hour, minute, 0, 0, loc).Unix())
}

func clockFrom(start float64) func() float64 {
	var mu sync.Mutex
	t := start
	return func() float64 {
		mu.Lock()
		defer mu.Unlock()
		t++
		return t
	}
}
