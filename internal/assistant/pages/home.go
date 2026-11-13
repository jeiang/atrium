package pages

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
)

const paletteThreads = 8

func (p *pages) agenda(w http.ResponseWriter, r *http.Request) {
	read, err := p.assistant().Home(r.Context(), asstapp.HomeInput{Agenda: true})
	if err != nil {
		p.readFailed(w, r, err)
		return
	}
	if web.FragmentNotModified(w, r, read.Revision) {
		return
	}
	p.fragment(w, r, http.StatusOK, "_agenda.gohtml", read.Data.Day)
}

type paletteData struct {
	Threads []asstapp.Listed `json:"threads"`
	Ref     bool             `json:"ref"`
}

func (p *pages) today(w http.ResponseWriter, r *http.Request) {
	changed, err := p.assistant().TodayEnsure(detached(r), asstapp.TodayInput{})
	check(err)
	redirect(w, r, "/chat/"+strconv.FormatInt(changed.Data.ThreadID, 10))
}

func (p *pages) palette(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q := strings.TrimSpace(web.QueryValue(query, "q"))
	ref, _ := strconv.ParseInt(web.QueryValue(query, "ref"), 10, 64)
	found := []asstapp.Listed{}
	read, err := p.assistant().ThreadList(r.Context(), asstapp.ThreadListInput{Q: q, Limit: paletteThreads + 1, Strict: true})
	if err == nil {
		found = read.Data.Items
	} else if !errorIs(err, application.Invalid) {
		check(err)
	}
	if ref != 0 {
		found = slices.DeleteFunc(found, func(t asstapp.Listed) bool { return t.ID == ref })
	}
	if len(found) > paletteThreads {
		found = found[:paletteThreads]
	}
	p.fragment(w, r, http.StatusOK, "_palette_threads.gohtml", paletteData{Threads: found, Ref: ref != 0})
}
