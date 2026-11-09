package pages

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/textsearch"
	"github.com/jeiang/atrium/internal/web"
)

const paletteThreads = 8

type paletteData struct {
	Threads []threads.Listed `json:"threads"`
	Ref     bool             `json:"ref"`
}

func (p *pages) today(w http.ResponseWriter, r *http.Request) {
	loc, err := config.From(r.Context()).Location(p.deps.Env)
	check(err)
	now := p.now()
	id, err := threads.Today(detached(r), p.deps.DB, now, time.Unix(int64(now), 0).In(loc))
	check(err)
	redirect(w, r, "/chat/"+strconv.FormatInt(id, 10))
}

func (p *pages) palette(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q := strings.TrimSpace(web.QueryValue(query, "q"))
	ref, _ := strconv.ParseInt(web.QueryValue(query, "ref"), 10, 64)
	var found []threads.Listed
	match, err := textsearch.FTSQuery(q)
	if q == "" || (err == nil && match != "") {
		found, err = threads.ListThreads(r.Context(), p.deps.DB.Read, threads.ListOpts{Match: match, Limit: paletteThreads + 1})
		check(err)
	}
	if ref != 0 {
		found = slices.DeleteFunc(found, func(t threads.Listed) bool { return t.ID == ref })
	}
	if len(found) > paletteThreads {
		found = found[:paletteThreads]
	}
	p.fragment(w, r, http.StatusOK, "_palette_threads.gohtml", paletteData{Threads: found, Ref: ref != 0})
}
