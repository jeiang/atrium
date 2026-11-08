package pages

import (
	"context"
	"math/bits"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/web"
)

type historyLine struct {
	ThreadID int64
	Text     string
	From     int64
	To       int64
	N        int64
	State    string
	Leaf     bool
}

type historyData struct {
	Lines []historyLine
}

type historyChildren struct {
	Children []historyLine
}

type historyLeaf struct {
	Text string
}

func (p *pages) hasHistory(ctx context.Context, threadID int64) bool {
	var n int
	if err := p.deps.DB.Read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND state IN (?, ?)", threadID, memory.Summary, memory.Fallback).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func (p *pages) history(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	v, err := memory.Build(r.Context(), p.deps.DB.Write, threadID, p.now())
	if err != nil || v == nil {
		p.fail(w, r, http.StatusNotFound, "unknown thread")
		return
	}
	lines := make([]historyLine, len(v.Lines))
	for i, l := range v.Lines {
		lines[i] = historyLine{ThreadID: threadID, Text: l.Text, From: l.From, To: l.To, N: 1 << l.Level, State: l.State, Leaf: l.Level == 0}
	}
	p.fragment(w, r, http.StatusOK, "_history.gohtml", historyData{Lines: lines})
}

func (p *pages) historyZoom(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	start, errs := pathInt(r, "start")
	n, more := pathInt(r, "n")
	if errs = append(errs, more...); len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if n < 1 || n&(n-1) != 0 {
		p.fail(w, r, http.StatusNotFound, "unknown range")
		return
	}
	nodes, err := memory.Tree(r.Context(), p.deps.DB.Read, threadID)
	if err != nil {
		p.fail(w, r, http.StatusNotFound, "unknown range")
		return
	}
	if n == 1 {
		text, ok := leafText(r.Context(), p, threadID, start, nodes)
		if !ok {
			p.fail(w, r, http.StatusNotFound, "unknown range")
			return
		}
		p.fragment(w, r, http.StatusOK, "_history_leaf.gohtml", historyLeaf{Text: text})
		return
	}
	level := bits.TrailingZeros64(uint64(n))
	var parent *memory.Node
	for i := range nodes {
		if nodes[i].Level == level && nodes[i].FromSeq == start && nodes[i].ToSeq == start+n-1 {
			parent = &nodes[i]
			break
		}
	}
	if parent == nil {
		p.fail(w, r, http.StatusNotFound, "unknown range")
		return
	}
	var children []historyLine
	for i := range nodes {
		if nodes[i].Level == level-1 && (nodes[i].Index == parent.Index*2 || nodes[i].Index == parent.Index*2+1) {
			children = append(children, historyLine{ThreadID: threadID, Text: nodes[i].Text, From: nodes[i].FromSeq, To: nodes[i].ToSeq,
				N: n / 2, State: nodes[i].State, Leaf: level-1 == 0})
		}
	}
	if len(children) != 2 {
		p.fail(w, r, http.StatusNotFound, "unknown range")
		return
	}
	p.fragment(w, r, http.StatusOK, "_history_children.gohtml", historyChildren{Children: children})
}

func leafText(ctx context.Context, p *pages, threadID, seq int64, nodes []memory.Node) (string, bool) {
	found := false
	for _, n := range nodes {
		if n.Level == 0 && n.FromSeq == seq {
			found = true
			break
		}
	}
	if !found {
		return "", false
	}
	messages, err := journal.MessagesOf(ctx, p.deps.DB.Read, threadID)
	if err != nil {
		return "", false
	}
	for _, m := range messages {
		if m.Seq != seq {
			continue
		}
		blocks, err := block.DecodeAll(m.Content)
		if err != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			if b.Kind == block.Text {
				parts = append(parts, b.Text())
			}
		}
		return strings.Join(parts, "\n"), true
	}
	return "", false
}
