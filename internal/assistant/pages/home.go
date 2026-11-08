package pages

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/search"
	"github.com/jeiang/atrium/internal/web"
)

const paletteThreads = 8

func (p *pages) today(w http.ResponseWriter, r *http.Request) {
	loc, err := config.From(r.Context()).Location(p.deps.Env)
	check(err)
	now := p.now()
	id, err := threads.Today(detached(r), p.deps.DB, now, time.Unix(int64(now), 0).In(loc))
	check(err)
	redirect(w, r, "/chat/"+strconv.FormatInt(id, 10))
}

func (p *pages) palette(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(web.QueryValue(r.URL.Query(), "q"))
	var found []threads.Listed
	match, err := search.FTSQuery(q)
	if q == "" || (err == nil && match != "") {
		found, err = threads.ListThreads(r.Context(), p.deps.DB.Read, threads.ListOpts{Match: match, Limit: paletteThreads})
		check(err)
	}
	p.fragment(w, r, http.StatusOK, "_palette_threads.gohtml", found)
}
