package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
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
	Ask   string `json:"ask"`
}

type agendaData struct {
	Events []agendaEvent `json:"events"`
	Next   *agendaEvent  `json:"next"`
}

func (p *pages) digestLines(ctx context.Context, day time.Time) []block.DigestLine {
	var thread int64
	err := p.deps.DB.Read.QueryRowContext(ctx,
		"SELECT id FROM threads WHERE kind = ? AND role = ? AND title = ? AND deleted_at IS NULL ORDER BY id LIMIT 1",
		threads.Chat, threads.TodayRole, threads.TodayTitle(day)).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	check(err)
	rows, err := p.deps.DB.Read.QueryContext(ctx, "SELECT content_json FROM thread_messages WHERE thread_id = ? ORDER BY seq DESC LIMIT 20", thread)
	check(err)
	bodies, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var body string
		return body, r.Scan(&body)
	})
	check(err)
	for _, body := range bodies {
		var content []json.RawMessage
		if json.Unmarshal([]byte(body), &content) != nil {
			continue
		}
		if _, lines, ok, err := block.DigestLines(content); err == nil && ok {
			return lines
		}
	}
	return nil
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
		event := agendaEvent{Time: "all day", Title: row.Title, Meta: row.Location, Ask: eventRef(now.Format(time.DateOnly), row.UID)}
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
	data := agendaData{Events: agendaOf(listing.Events, tz, now)}
	for i, event := range data.Events {
		if event.State != "past" && event.Time != "all day" {
			data.Next = &data.Events[i]
			break
		}
	}
	p.fragment(w, r, http.StatusOK, "_agenda.gohtml", data)
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
