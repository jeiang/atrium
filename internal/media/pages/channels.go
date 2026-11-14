package pages

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) registerChannels(r chi.Router) {
	r.Get("/apps/media/channels", s.channelsIndex)
	r.Post("/apps/media/channels", s.channelAdd)
	r.Post("/apps/media/channels/preview", s.channelPreview)
	r.Post("/apps/media/channels/{channel_id}/delete", s.channelDelete)
	r.Post("/apps/media/channels/poll", s.channelPoll)
}

func chToast(category, title, description string) *tpl.Toast {
	return &tpl.Toast{Category: category, Title: title, Description: description}
}

func (s *service) chPage(w http.ResponseWriter, r *http.Request, notice *tpl.Toast, status int, q string, form *ChannelForm, results []SearchHit, searched bool) {
	read, err := s.app.Channels(r.Context(), mediaapp.ChannelsInput{})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return
	}
	channels := make([]ChannelView, len(read.Data.Channels))
	for i, c := range read.Data.Channels {
		channels[i] = ChannelView{ID: c.ID, Name: c.Name, IMDb: c.IMDB, Pattern: c.Pattern, CaseSensitive: c.CaseSensitive, Primed: c.Primed}
	}
	data := ChannelsPage{
		Channels: channels,
		Status:   ChannelStatusView{LastCheck: read.Data.Status.LastCheck, Errors: read.Data.Status.Errors},
		TMDB:     read.Data.TMDB,
		Q:        q,
		Results:  results,
		Searched: searched,
	}
	if form != nil {
		data.Form = *form
	}
	f, err := s.deps.Render.Frame(r, "media_channels", data)
	if err != nil {
		panic(err)
	}
	f.Toast = notice
	if err := s.deps.Render.Page(w, r, status, "media_channels.gohtml", f); err != nil {
		panic(err)
	}
}

func patternBase(name string) string {
	return "^" + regexp.QuoteMeta(name) + `( \d{4})? S\d+E\d+`
}

func tmdbSkipped(err error) bool {
	return application.Classify(err).Code == application.NotConfigured
}

func (s *service) channelsIndex(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q := strings.TrimSpace(web.QueryValue(query, "q"))
	imdb := strings.TrimSpace(web.QueryValue(query, "imdb"))
	name := web.QueryValue(query, "name")
	tmdbID := strings.TrimSpace(web.QueryValue(query, "tmdb_id"))
	pattern := web.QueryValue(query, "pattern")
	var results []SearchHit
	searched := false
	var notice *tpl.Toast
	err := func() error {
		ctx := r.Context()
		if q != "" {
			found, err := s.app.ShowSearch(ctx, mediaapp.ShowSearchInput{Query: q})
			if err != nil && !tmdbSkipped(err) {
				return err
			}
			if err == nil {
				results = make([]SearchHit, len(found.Data))
				for i, f := range found.Data {
					results[i] = SearchHit{TmdbID: f.TmdbID, Name: f.Name, Year: f.Year}
				}
				searched = true
			}
		}
		if id, ok := parseInt(tmdbID); isDigits(tmdbID) && ok && imdb == "" {
			show, err := s.app.ShowLookup(ctx, mediaapp.ShowLookupInput{TMDBID: id})
			if err != nil && !tmdbSkipped(err) {
				return err
			}
			if err == nil && show.Data.Found {
				imdb = show.Data.IMDB
			} else if err == nil {
				notice = chToast("error", "No IMDb id on TMDB", "Enter it by hand.")
			}
		}
		return nil
	}()
	if err != nil {
		failure := application.Classify(err)
		if failure.Code != application.Unavailable {
			panic(err)
		}
		notice = chToast("error", "TMDB failed", failure.Error())
	}
	form := ChannelForm{IMDb: imdb, Name: strings.TrimSpace(name), TmdbID: tmdbID, Pattern: pattern}
	if form.Name != "" {
		form.Base = patternBase(form.Name)
		if form.Pattern == "" {
			form.Pattern = form.Base + " 1080p"
		}
	}
	s.chPage(w, r, notice, http.StatusOK, q, &form, results, searched)
}

func (s *service) channelAdd(w http.ResponseWriter, r *http.Request) {
	values := web.FormValues(r)
	name := web.FormString(values, "name", "")
	imdb := web.FormString(values, "imdb", "")
	tmdbID := web.FormString(values, "tmdb_id", "")
	pattern := web.FormString(values, "pattern", "")
	caseSensitive := web.FormString(values, "case_sensitive", "") != ""
	backfill := web.FormString(values, "backfill", "") != ""
	form := ChannelForm{TmdbID: tmdbID, Name: name, IMDb: imdb, Pattern: pattern, CaseSensitive: caseSensitive, Backfill: backfill}
	var tmdb *int64
	if trimmed := strings.TrimSpace(tmdbID); isDigits(trimmed) {
		if id, ok := parseInt(trimmed); ok {
			tmdb = &id
		}
	}
	added, err := s.app.ChannelAdd(r.Context(), mediaapp.ChannelAddInput{Name: name, IMDB: imdb, TMDBID: tmdb, Pattern: pattern, CaseSensitive: caseSensitive, Backfill: backfill})
	if err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) {
			s.chPage(w, r, chToast("error", "Not saved", failure.Error()), status, "", &form, nil, false)
		})
		return
	}
	title := "Watching " + added.Data.Channel.Name
	switch {
	case !added.Data.Polled:
		s.chPage(w, r, chToast("success", title, "The first poll only records what is already out; later matches are downloaded."), http.StatusOK, "", nil, nil, false)
	case added.Data.PollFail != nil:
		poll := added.Data.Poll
		poll.Errors = append(poll.Errors, added.Data.PollFail.Error())
		s.chPage(w, r, chPolled(title, "nothing downloaded", poll), http.StatusOK, "", nil, nil, false)
	default:
		s.chPage(w, r, chPolled(title, "no matching releases yet", added.Data.Poll), http.StatusOK, "", nil, nil, false)
	}
}

func chPolled(title, idle string, out mediaapp.PollResult) *tpl.Toast {
	titles := make([]string, len(out.Added))
	for i, a := range out.Added {
		titles[i] = a.Title
	}
	added := strings.Join(titles, ", ")
	if added == "" {
		added = idle
	}
	errs := ""
	if len(out.Errors) > 0 {
		errs = " Errors: " + strings.Join(out.Errors, "; ")
	}
	category := "success"
	if len(out.Errors) > 0 {
		category = "error"
	}
	return chToast(category, title, "Added: "+added+"."+errs)
}

func (s *service) channelPreview(w http.ResponseWriter, r *http.Request) {
	values := web.FormValues(r)
	in := mediaapp.ReleasePreviewInput{
		IMDB:          web.FormString(values, "imdb", ""),
		Pattern:       web.FormString(values, "pattern", ""),
		CaseSensitive: web.FormString(values, "case_sensitive", "") != "",
	}
	view := PreviewView{}
	read, err := s.app.ReleasePreview(r.Context(), in)
	if err != nil {
		failure := application.Classify(err)
		if failure.Code != application.Invalid && failure.Code != application.Unavailable {
			panic(err)
		}
		view.Error = failure.Error()
	} else {
		releases := make([]ReleaseView, len(read.Data.Releases))
		for i, row := range read.Data.Releases {
			releases[i] = ReleaseView{Title: row.Title, Match: row.Match, SizeBytes: row.SizeBytes}
		}
		view.Releases, view.Matched = releases, read.Data.Matched
	}
	if err := s.deps.Render.Fragment(w, r, http.StatusOK, "_media_preview.gohtml", view); err != nil {
		panic(err)
	}
}

func (s *service) channelDelete(w http.ResponseWriter, r *http.Request) {
	raw := web.PathParam(r, "channel_id")
	id, ok := laxInt64(raw)
	if !ok {
		web.Unprocessable(w, web.BadInt("path", "channel_id", raw))
		return
	}
	removed, err := s.app.ChannelRemove(r.Context(), mediaapp.ChannelRemoveInput{ID: id})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return
	}
	s.chPage(w, r, chToast("success", "Removed "+removed.Data.Channel.Name, ""), http.StatusOK, "", nil, nil, false)
}

func (s *service) channelPoll(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.Poll(r.Context(), mediaapp.PollInput{})
	if err != nil {
		failure := application.Classify(err)
		if len(out.Data.Result.Added) > 0 {
			poll := out.Data.Result
			poll.Errors = append(poll.Errors, failure.Error())
			s.chPage(w, r, chPolled("Poll stopped", "nothing new", poll), http.StatusUnprocessableEntity, "", nil, nil, false)
			return
		}
		web.Fail(w, r, failure, func(status int) {
			if status == http.StatusInternalServerError {
				s.deps.Error(w, r, status, failure.Error())
				return
			}
			s.chPage(w, r, chToast("error", "Not polled", failure.Error()), http.StatusUnprocessableEntity, "", nil, nil, false)
		})
		return
	}
	s.chPage(w, r, chPolled("Polled", "nothing new", out.Data.Result), http.StatusOK, "", nil, nil, false)
}
