package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const runCodeSummary = " For several read-only lookups across sources, call run_code once instead of many tool turns."

func wall(year int, month time.Month, day, hour, minute int) float64 {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		panic(err)
	}
	return float64(time.Date(year, month, day, hour, minute, 0, 0, loc).Unix())
}

func stableRunner(t *testing.T) *loop.Runner {
	t.Helper()
	stab := registrytest.Load(t)
	reg := registry.New()
	if err := reg.Add(stab.Stubs(t)...); err != nil {
		t.Fatal(err)
	}
	return &loop.Runner{
		Registry:    reg,
		Profile:     func(context.Context, db.Querier) (string, error) { return stab.SystemTexts["profile"], nil },
		SkillsIndex: func(context.Context, db.Querier) (string, error) { return stab.SystemTexts["skills_index"], nil },
	}
}

func canonicalBody(t *testing.T, raw []byte, recorded bool) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if system, ok := body["system"].([]any); ok && recorded {
		first := system[0].(map[string]any)
		first["text"] = strings.Replace(first["text"].(string), runCodeSummary, "", 1)
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func replayScenario(t *testing.T, name string, drive func(s *site)) {
	scenario := golden.LoadScenario(t, "anthropic", name)
	s := newSite(t, withRunner(stableRunner(t)))
	for _, ex := range scenario.Exchanges {
		resp, err := anthropictest.FromGolden(ex.Response)
		if err != nil {
			t.Fatal(err)
		}
		s.api.Push(resp)
	}
	drive(s)
	got := s.api.Requests()
	if len(got) != len(scenario.Exchanges) {
		t.Fatalf("%d requests, recorded %d", len(got), len(scenario.Exchanges))
	}
	for i, ex := range scenario.Exchanges {
		for _, h := range []string{"anthropic-beta", "anthropic-version", "content-type"} {
			if g := strings.Join(got[i].Header.Values(h), ","); g != ex.Request.Headers[h] {
				t.Errorf("request %d header %s = %q, recorded %q", i, h, g, ex.Request.Headers[h])
			}
		}
		golden.EqualJSON(t, canonicalBody(t, ex.Request.Body, true), canonicalBody(t, got[i].Body, false))
	}
}

func TestWebDrivenTurnsMatchRecordedRequests(t *testing.T) {
	t.Run("chat_followup_turn", func(t *testing.T) {
		replayScenario(t, "chat_followup_turn", func(s *site) {
			s.setClock(wall(2026, time.October, 15, 12, 0))
			thread := s.startThread("hello")
			s.settle(s.lastRun(thread))
			s.setClock(wall(2026, time.November, 2, 9, 30))
			status(t, s.multipart(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "are you there?"}, nil, hx, "true"), http.StatusOK)
			s.settle(s.lastRun(thread))
		})
	})
	t.Run("project_thread", func(t *testing.T) {
		replayScenario(t, "project_thread", func(s *site) {
			at := wall(2026, time.October, 15, 12, 0)
			project, err := projects.Create(context.Background(), s.db.Write, at, projects.Fields{
				Name: "Garden", Instructions: "Answer in metric units.", Effort: threads.High, ContextLimit: int64(200_000), WikiHubID: "0f1e2d3c4b5a69788796a5b4c3d2e1f0",
			})
			if err != nil {
				t.Fatal(err)
			}
			s.setClock(at)
			r := s.multipart("/chat", map[string]string{"text": "How far apart should tomatoes go?", "project_id": fmt.Sprint(project)}, nil, hx, "true")
			status(t, r, http.StatusNoContent)
			var thread int64
			if _, err := fmt.Sscanf(r.Header.Get("HX-Redirect"), "/chat/%d", &thread); err != nil {
				t.Fatal(err)
			}
			s.settle(s.lastRun(thread))
		})
	})
}
