package pages

import (
	"net/http"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
)

type historyData struct {
	Lines []asstapp.HistoryLine
}

type historyChildren struct {
	Children []asstapp.HistoryLine
}

type historyLeaf struct {
	Text string
}

func (p *pages) history(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	read, err := p.assistant().History(r.Context(), asstapp.HistoryInput{ThreadID: threadID})
	if err != nil {
		p.readFailed(w, r, err)
		return
	}
	p.fragment(w, r, http.StatusOK, "_history.gohtml", historyData{Lines: read.Data.Lines})
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
	if n < 1 {
		p.fail(w, r, http.StatusNotFound, "unknown range")
		return
	}
	read, err := p.assistant().History(r.Context(), asstapp.HistoryInput{ThreadID: threadID, Start: start, N: n})
	if err != nil {
		p.readFailed(w, r, err)
		return
	}
	if web.FragmentNotModified(w, r, read.Revision) {
		return
	}
	if read.Data.Leaf != nil {
		p.fragment(w, r, http.StatusOK, "_history_leaf.gohtml", historyLeaf{Text: *read.Data.Leaf})
		return
	}
	p.fragment(w, r, http.StatusOK, "_history_children.gohtml", historyChildren{Children: read.Data.Lines})
}
