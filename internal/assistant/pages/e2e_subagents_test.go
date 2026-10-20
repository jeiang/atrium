package pages

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func tickedTools(card string) []string {
	const open = `<input type="checkbox" name="tools" value="`
	var out []string
	rest := card
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			return out
		}
		name := rest[:j]
		rest = rest[j:]
		if strings.HasPrefix(rest, `" class="input" checked>`) {
			out = append(out, name)
		}
	}
}

func agentCard(t *testing.T, page, name string) string {
	t.Helper()
	start := strings.Index(page, `data-agent="`+name+`"`)
	if start < 0 {
		t.Fatalf("no card for %s", name)
	}
	end := strings.Index(page[start:], "</details>")
	if end < 0 {
		t.Fatalf("card %s is not closed", name)
	}
	return page[start : start+end]
}

func (s *site) agentRow(name string) (description, prompt, effort string, tools []string) {
	s.t.Helper()
	var stored string
	if err := s.db.Read.QueryRow("SELECT description, prompt, effort, tools_json FROM subagents WHERE name = ?", name).Scan(&description, &prompt, &effort, &stored); err != nil {
		s.t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stored), &tools); err != nil {
		s.t.Fatal(err)
	}
	slices.Sort(tools)
	return description, prompt, effort, tools
}

func TestBuiltinSubagentCardsKeepRecallOnSave(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/settings/subagents"), http.StatusOK)
	mustContain(t, page, `<code class="font-mono">recall</code>`)
	for _, name := range []string{"mail", "web"} {
		description, prompt, effort, stored := s.agentRow(name)
		ticked := tickedTools(agentCard(t, page, name))
		if !slices.Contains(stored, "recall") || !slices.Equal(ticked, stored) {
			t.Errorf("%s card ticks %v, stored %v", name, ticked, stored)
		}
		form := url.Values{"name": {name}, "description": {description}, "prompt": {prompt}, "effort": {effort}, "tools": ticked}
		response := s.post("/settings/subagents", form)
		location := "/settings/subagents?saved=" + url.QueryEscape(name)
		if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != location || response.body != "" {
			t.Fatalf("%s save: %d %q %q", name, response.StatusCode, response.Header.Get("Location"), response.body)
		}
		mustContain(t, status(t, s.get(location), http.StatusOK), "Subagent saved")
		if _, _, _, saved := s.agentRow(name); !slices.Equal(saved, stored) {
			t.Errorf("%s tools after save %v, before %v", name, saved, stored)
		}
	}
}
