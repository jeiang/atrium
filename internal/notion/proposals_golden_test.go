package notion

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func proposalWorld(t *testing.T, scenario, markdown string) (*Proposer, *proposals.Pipeline) {
	t.Helper()
	exchanges := golden.LoadScenario(t, "notion", scenario).Exchanges
	srv := golden.Replay(t, exchanges)
	var first struct {
		Body struct {
			LastEdited string `json:"last_edited_time"`
		} `json:"body"`
	}
	if err := json.Unmarshal(exchanges[0].Response, &first); err != nil {
		t.Fatal(err)
	}
	d := dbtest.Schema(t)
	for _, p := range [][3]string{{pageA, "Alpha plan", markdown}, {pageB, "Beta notes", "beta retrospective about alpha"}} {
		if _, err := d.Write.Exec(
			"INSERT INTO notion_pages (id, title, markdown, url, last_edited_time) VALUES (?, ?, ?, ?, ?)",
			p[0], p[1], p[2], "https://notion.invalid/"+p[0], first.Body.LastEdited); err != nil {
			t.Fatal(err)
		}
	}
	now := func() float64 { return 1_780_000_000 }
	registry := proposals.NewRegistry()
	(&Applier{
		DB: d, Env: config.Map(map[string]string{"NOTION_TOKEN": goldenToken}), Now: now,
		NewClient: func(token string) *Client { return fastClient(srv.URL, token, nil) },
	}).Register(registry)
	proposer := &Proposer{DB: d, Now: now}
	return proposer, &proposals.Pipeline{DB: d, Registry: registry, Now: now}
}

func approve(t *testing.T, pipe *proposals.Pipeline, id *int64) {
	t.Helper()
	if id == nil {
		t.Fatal("no proposal made")
	}
	if got, err := pipe.ApproveAndApply(t.Context(), *id, nil); err != nil || got.Status != proposals.Applied {
		t.Fatalf("ApproveAndApply = %+v, %v", got, err)
	}
}

func chatReturning(answer string) ChatFunc {
	return func(context.Context, string, string, string, any, int) (json.RawMessage, error) {
		return json.RawMessage(answer), nil
	}
}

func TestGoldenProposalLinkAppend(t *testing.T) {
	p, pipe := proposalWorld(t, "proposal_link_append", "alpha roadmap")
	p.Chat = chatReturning(`{"link": true, "reason": "same project"}`)
	id, err := p.ProposeLink(t.Context(), pageA, pageB)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, pipe, id)
	if undone, err := pipe.Undo(t.Context(), *id); err != nil || !undone.Undone {
		t.Fatalf("Undo = %+v, %v", undone, err)
	}
}

func TestGoldenProposalLinkRelatedSection(t *testing.T) {
	p, pipe := proposalWorld(t, "proposal_link_related_section", "alpha roadmap\n\n## Related\n- [Gamma](https://notion.invalid/gamma)\n\n## Later\n\nx")
	p.Chat = chatReturning(`{"link": true, "reason": "same project"}`)
	id, err := p.ProposeLink(t.Context(), pageA, pageB)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, pipe, id)
}

func TestGoldenProposalEdit(t *testing.T) {
	p, pipe := proposalWorld(t, "proposal_edit", "Launch in June.\n\nOwner: Sam")
	p.Chat = chatReturning(`{"updates": [{"old_str": "Launch in June.", "new_str": "Launch in July."}], "summary": "Moves the launch to July."}`)
	id, err := p.ProposeEdit(t.Context(), pageA, "move to july")
	if err != nil {
		t.Fatal(err)
	}
	approve(t, pipe, id)
}
