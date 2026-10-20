package search

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/search.py:TOKEN": tokenRe,
		"src/atrium/search.py:WORD":  wordRe,
	})
}

type fields = rulestest.Fields

func hitIDs(r Result) []int64 {
	out := []int64{}
	for _, h := range r.Hits {
		out = append(out, h.ID)
	}
	return out
}

func fakeEmbed(vector []float32) embed.TextsFunc {
	return func(_ context.Context, _ string, texts []string) ([][]float64, error) {
		out := make([][]float64, len(texts))
		for i := range texts {
			v := make([]float64, len(vector))
			for j, x := range vector {
				v[j] = float64(x)
			}
			out[i] = v
		}
		return out, nil
	}
}

func down(context.Context, string, []string) ([][]float64, error) {
	return nil, &httpx.ServiceUnavailable{Msg: "offline"}
}

func run(t *testing.T, d *db.DB, query string, filters map[string]any, limit int, e embed.TextsFunc) Result {
	t.Helper()
	r, err := Search(t.Context(), d.Read, config.Config{}, query, filters, limit, e)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFTSQueryEscapesAndPrefixes(t *testing.T) {
	for in, want := range map[string]string{
		`hello world`:              `"hello"* "world"*`,
		`foo" OR 1=1 -- NEAR(`:     `"foo"* "OR"* "1"* "1"* "NEAR"*`,
		`"exact phrase" tail`:      `"exact phrase" "tail"*`,
		`"single"`:                 `"single"`,
		"a_b c*d":                  `"a"* "b"* "c"* "d"*`,
		"   ":                      "",
		`""`:                       "",
		`"a_b  c"`:                 `"a b c"`,
		strings.Repeat("www ", 40): strings.TrimSpace(strings.Repeat(`"www"* `, MaxTerms)),
		"naïve café":               `"naïve"* "café"*`,
	} {
		got, err := FTSQuery(in)
		if err != nil || got != want {
			t.Errorf("FTSQuery(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestLexicalOnlyWhenEmbedDown(t *testing.T) {
	d := rulestest.DB(t)
	a := rulestest.Add(t, d, 1, nil, "please pay the invoice today", fields{"from_addr": "a@x.test", "from_domain": "example.test", "folder": "Alpha",
		"location": "Alpha", "in_inbox": 0, "subject": "Invoice march"})
	rulestest.Add(t, d, 2, nil, "sandwich", fields{"from_addr": "b@x.test", "from_domain": "example.test", "folder": "Alpha",
		"location": "Alpha", "in_inbox": 0, "subject": "Lunch"})
	r := run(t, d, "invo", nil, DefaultLimit, down)
	if !slices.Equal(hitIDs(r), []int64{a}) || r.Semantic || r.Error == nil || !strings.Contains(*r.Error, "offline") {
		t.Fatalf("result = %+v", r)
	}
	h := r.Hits[0]
	if h.LexicalRank == nil || *h.LexicalRank != 1 || h.SemanticRank != nil {
		t.Errorf("ranks = %v %v", h.LexicalRank, h.SemanticRank)
	}
	highlighted := false
	for _, p := range h.SnippetParts {
		highlighted = highlighted || p.Highlighted && strings.Contains(strings.ToLower(p.Text), "invoice")
	}
	if !highlighted || strings.Contains(h.Snippet, "\x02") {
		t.Errorf("snippet = %q %v", h.Snippet, h.SnippetParts)
	}
}

func TestMalformedEmbeddingsAnswerDegradesToLexical(t *testing.T) {
	d := rulestest.DB(t)
	rulestest.Filed(t, d, 1, "Alpha", "a@x.test", "example.test", nil, fields{"subject": "topic"})
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: embedding response shape mismatch: expected 1x768", httpx.ErrValue), "embedding response shape mismatch: expected 1x768"},
		{fmt.Errorf("%w: 'data'", httpx.ErrKey), "'data'"},
	} {
		r := run(t, d, "topic", nil, DefaultLimit, func(context.Context, string, []string) ([][]float64, error) { return nil, c.err })
		if r.Semantic || r.Error == nil || *r.Error != c.want {
			t.Errorf("result = %+v %v", r, r.Error)
		}
	}
	other := errors.New("boom")
	if _, err := Search(t.Context(), d.Read, config.Config{}, "topic", nil, DefaultLimit,
		func(context.Context, string, []string) ([][]float64, error) { return nil, other }); !errors.Is(err, other) {
		t.Errorf("other errors propagate, got %v", err)
	}
}

func TestHybridFusesAndKeepsBranchRanks(t *testing.T) {
	d := rulestest.DB(t)
	filed := func(uid int, addr, subject, body string, v []float32) int64 {
		return rulestest.Add(t, d, uid, v, body, fields{"from_addr": addr, "from_domain": "example.test", "folder": "Alpha", "location": "Alpha",
			"in_inbox": 0, "subject": subject})
	}
	lexOnly := filed(1, "a@x.test", "report", "report", rulestest.Vector(5))
	both := filed(2, "b@x.test", "report summary", "report", rulestest.Vector(1))
	semOnly := filed(3, "c@x.test", "other", "nothing", rulestest.Unit(1, 2, 0.1))
	r := run(t, d, "report", nil, DefaultLimit, fakeEmbed(rulestest.Vector(1)))
	if !r.Semantic || hitIDs(r)[0] != both {
		t.Fatalf("result = %+v", r)
	}
	by := map[int64]Hit{}
	for _, h := range r.Hits {
		by[h.ID] = h
	}
	if by[lexOnly].SemanticRank == nil {
		t.Error("the lexical-only message is still in the vector ranking")
	}
	s := by[semOnly]
	if s.LexicalRank != nil || s.SemanticRank == nil || *s.SemanticRank != 2 || s.Snippet != "nothing" {
		t.Errorf("semantic-only hit = %+v", s)
	}
}

func TestQueryPromptUsed(t *testing.T) {
	d := rulestest.DB(t)
	var seen []string
	capture := func(_ context.Context, _ string, texts []string) ([][]float64, error) {
		seen = append(seen, texts...)
		return fakeEmbed(rulestest.Vector(0))(t.Context(), "", texts)
	}
	run(t, d, "where is my parcel", nil, DefaultLimit, capture)
	if !slices.Equal(seen, []string{"task: search result | query: where is my parcel"}) {
		t.Errorf("embedded %q", seen)
	}
}

func TestFiltersApply(t *testing.T) {
	d := rulestest.DB(t)
	base := func(f fields) fields {
		out := fields{"subject": "topic"}
		for k, v := range f {
			out[k] = v
		}
		return out
	}
	a := rulestest.Add(t, d, 1, nil, "topic", base(fields{"from_addr": "ann@one.test", "from_domain": "one.test", "folder": "Alpha", "location": "Alpha",
		"in_inbox": 0, "date_ts": 1_700_000_000, "seen": 1}))
	b := rulestest.Add(t, d, 2, nil, "topic", base(fields{"from_addr": "bob@two.test", "from_domain": "two.test", "folder": "Beta", "location": "Beta",
		"in_inbox": 0, "date_ts": 1_800_000_000, "seen": 0, "flagged": 1, "att_types": []string{"application/pdf"}}))
	g := rulestest.Add(t, d, 3, nil, "topic", base(fields{"account": "gmail", "mailbox_id": 2, "from_addr": "cy@one.test", "from_domain": "one.test",
		"folder": "Gamma", "location": "Gamma", "date_ts": 1_750_000_000}))
	ids := func(f map[string]any) []int64 {
		got := hitIDs(run(t, d, "topic", f, DefaultLimit, down))
		slices.Sort(got)
		return got
	}
	for _, c := range []struct {
		name string
		f    map[string]any
		want []int64
	}{
		{"none", nil, []int64{a, b, g}},
		{"account", map[string]any{"account": "gmail"}, []int64{g}},
		{"folder", map[string]any{"folder": "Beta"}, []int64{b}},
		{"from domain", map[string]any{"from": "ONE.test"}, []int64{a, g}},
		{"from addr", map[string]any{"from": "bob@"}, []int64{b}},
		{"date_from", map[string]any{"date_from": "2025-01-01"}, []int64{b, g}},
		{"date_to", map[string]any{"date_to": "2023-11-15"}, []int64{a}},
		{"date number", map[string]any{"date_from": 1_760_000_000.0}, []int64{b}},
		{"attachment", map[string]any{"has_attachment": true}, []int64{b}},
		{"unread", map[string]any{"unread": "on"}, []int64{b, g}},
		{"flagged", map[string]any{"flagged": "1"}, []int64{b}},
		{"combined", map[string]any{"account": "icloud", "unread": true, "flagged": true}, []int64{b}},
		{"blank", map[string]any{"folder": "  ", "unread": "no"}, []int64{a, b, g}},
	} {
		if got := ids(c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := NormalizeFilters(map[string]any{"date_from": "yesterday"}); !errors.Is(err, httpx.ErrValue) {
		t.Errorf("bad date: %v", err)
	}
}

func noiseAndTarget(t *testing.T, d *db.DB) int64 {
	t.Helper()
	for i := range 30 {
		rulestest.Filed(t, d, 100+i, "Noise", fmt.Sprintf("n%d@x.test", i), "example.test", rulestest.Unit(1, 2, 0.01*float32(i)), fields{"subject": "n"})
	}
	return rulestest.Filed(t, d, 1, "Target", "t@x.test", "example.test", rulestest.Vector(3), fields{"subject": "t"})
}

func TestFiltersApplyToVectorBranch(t *testing.T) {
	d := rulestest.DB(t)
	target := noiseAndTarget(t, d)
	r := run(t, d, "zzzz", map[string]any{"folder": "Target"}, DefaultLimit, fakeEmbed(rulestest.Vector(1)))
	if !slices.Equal(hitIDs(r), []int64{target}) {
		t.Errorf("hits = %v", hitIDs(r))
	}
}

func TestVectorBranchFallsBackToExactPastKNNCap(t *testing.T) {
	old := knnMax
	knnMax = 8
	t.Cleanup(func() { knnMax = old })
	d := rulestest.DB(t)
	target := noiseAndTarget(t, d)
	r := run(t, d, "zzzz", map[string]any{"folder": "Target"}, DefaultLimit, fakeEmbed(rulestest.Vector(1)))
	if !slices.Equal(hitIDs(r), []int64{target}) {
		t.Errorf("hits = %v", hitIDs(r))
	}
}

func TestCorrectedFolderIsSearchable(t *testing.T) {
	d := rulestest.DB(t)
	m := rulestest.Inbox(t, d, 1, "new@sender.test", "sender.test", nil, fields{"subject": "topic"})
	rulestest.Exec(t, d, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Moved', 1, 'operator')", m)
	r := run(t, d, "topic", map[string]any{"folder": "Moved"}, DefaultLimit, down)
	if !slices.Equal(hitIDs(r), []int64{m}) || r.Hits[0].Folder != "Moved" {
		t.Errorf("result = %+v", r)
	}
}

func TestEmptyQueryListsRecent(t *testing.T) {
	d := rulestest.DB(t)
	old := rulestest.Add(t, d, 1, nil, "old body", fields{"from_addr": "a@x.test", "from_domain": "x", "folder": "A", "location": "A", "in_inbox": 0, "date_ts": 1.0})
	newer := rulestest.Add(t, d, 2, nil, "new   body", fields{"from_addr": "b@x.test", "from_domain": "x", "folder": "A", "location": "A", "in_inbox": 0, "date_ts": 2.0})
	r := run(t, d, "", nil, 5, nil)
	if !slices.Equal(hitIDs(r), []int64{newer, old}) || r.Hits[0].Snippet != "new body" || r.Semantic || r.Error != nil || r.Hits[0].Score != nil {
		t.Errorf("result = %+v", r)
	}
}

func TestHostileQueryDoesNotFail(t *testing.T) {
	d := rulestest.DB(t)
	rulestest.Filed(t, d, 1, "Alpha", "a@x.test", "example.test", nil, fields{"subject": "hello"})
	for _, q := range []string{`"`, "AND", "NEAR(", "col:x", "-", "*", "'; DROP TABLE messages;--"} {
		if _, err := Search(t.Context(), d.Read, config.Config{}, q, nil, DefaultLimit, down); err != nil {
			t.Errorf("Search(%q): %v", q, err)
		}
	}
}

func TestRRFOrder(t *testing.T) {
	fused := RRF([]int64{1, 2, 3}, []int64{3, 4, 1})
	ids := []int64{}
	for _, s := range fused {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []int64{1, 3, 2, 4}) {
		t.Errorf("order = %v", ids)
	}
	if fused[0].Score != 1.0/61+1.0/63 {
		t.Errorf("score = %v", fused[0].Score)
	}
	if got := RRF([]int64{5}, []int64{6})[0].ID; got != 5 {
		t.Errorf("tie went to %d", got)
	}
}
