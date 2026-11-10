package pages

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/textsearch"
	"github.com/jeiang/atrium/internal/web"
)

const (
	paletteThreads = 8
	allThreads     = "all"
)

type agendaEvent struct {
	Time  string `json:"time"`
	Title string `json:"title"`
	Meta  string `json:"meta"`
	State string `json:"state"`
}

type agendaData struct {
	Events []agendaEvent `json:"events"`
}

func span(d time.Duration) string {
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return strconv.Itoa(m) + " min"
	case m == 0:
		return strconv.Itoa(h) + " h"
	}
	return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
}

func agendaOf(rows []calendar.Row, tz *time.Location, now time.Time) []agendaEvent {
	out := make([]agendaEvent, 0, len(rows))
	for _, row := range rows {
		fields := row.Dict(tz)
		first, _ := fields["start"].(string)
		last, _ := fields["end"].(string)
		event := agendaEvent{Time: "all day", Title: row.Title, Meta: row.Location}
		start, err := time.ParseInLocation("2006-01-02T15:04:05", first[:min(len(first), 19)], tz)
		end, endErr := time.ParseInLocation("2006-01-02T15:04:05", last[:min(len(last), 19)], tz)
		if err == nil {
			event.Time = start.Format("15:04")
			if endErr == nil && end.After(start) {
				event.Meta = strings.TrimPrefix(row.Location+" · "+span(end.Sub(start)), " · ")
				switch {
				case !now.Before(end):
					event.State = "past"
				case !now.Before(start):
					event.State = "now"
				}
			}
		}
		out = append(out, event)
	}
	return out
}

func (p *pages) agenda(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !module.IsEnabled(p.deps.Ctx, module.Calendar) {
		p.fail(w, r, http.StatusNotFound, "calendar is off")
		return
	}
	tz, err := calendar.Zone(ctx, p.deps.Env)
	check(err)
	set, err := calendar.LoadSettings(ctx, p.deps.DB.Read)
	check(err)
	now := time.Unix(int64(p.now()), 0).In(tz)
	listing, err := calendar.ListEvents(ctx, p.deps.Env, set, now.Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly), calendar.Filter{})
	check(err)
	p.fragment(w, r, http.StatusOK, "_agenda.gohtml", agendaData{Events: agendaOf(listing.Events, tz, now)})
}

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
