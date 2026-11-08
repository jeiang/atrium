package pages

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) registerChannels(r chi.Router) {
	r.Get("/media/channels", s.channelsIndex)
	r.Post("/media/channels", s.channelAdd)
	r.Post("/media/channels/preview", s.channelPreview)
	r.Post("/media/channels/{channel_id}/delete", s.channelDelete)
	r.Post("/media/channels/poll", s.channelPoll)
}

func chToast(category, title, description string) *tpl.Toast {
	return &tpl.Toast{Category: category, Title: title, Description: description}
}

func (s *service) chPage(w http.ResponseWriter, r *http.Request, notice *tpl.Toast, status int, q string, form *ChannelForm, results []SearchHit, searched bool) {
	ctx := r.Context()
	set := s.settings(ctx)
	rows, err := media.ListChannels(ctx, s.deps.DB.Read)
	if err != nil {
		panic(err)
	}
	state, err := media.ChannelStatus(ctx, s.deps.DB.Read)
	if err != nil {
		panic(err)
	}
	channels := make([]ChannelView, len(rows))
	for i, c := range rows {
		channels[i] = ChannelView{ID: c.ID, Name: c.Name, IMDb: c.IMDB, Pattern: c.Pattern, CaseSensitive: c.CaseSensitive, Primed: c.Primed}
	}
	errs := state.Errors
	if errs == nil {
		errs = []string{}
	}
	data := ChannelsPage{
		Channels: channels,
		Status:   ChannelStatusView{LastCheck: state.LastCheck, Errors: errs},
		TMDB:     set.TMDBToken != "",
		Q:        q,
		Results:  results,
		Searched: searched,
		Form:     ChannelForm{Backfill: true},
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

func (s *service) channelsIndex(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q := strings.TrimSpace(web.QueryValue(query, "q"))
	imdb := strings.TrimSpace(web.QueryValue(query, "imdb"))
	name := web.QueryValue(query, "name")
	tmdbID := strings.TrimSpace(web.QueryValue(query, "tmdb_id"))
	pattern := web.QueryValue(query, "pattern")
	set := s.settings(r.Context())
	client := set.TMDB()
	var results []SearchHit
	searched := false
	var notice *tpl.Toast
	err := func() error {
		ctx := r.Context()
		if q != "" && client != nil {
			found, err := client.SearchTV(ctx, q, set.TMDBSearchLimit)
			if err != nil {
				return err
			}
			results = make([]SearchHit, len(found))
			for i, f := range found {
				results[i] = SearchHit{TmdbID: f.TmdbID, Name: f.Name, Year: f.Year}
			}
			searched = true
		}
		if id, ok := parseInt(tmdbID); isDigits(tmdbID) && ok && imdb == "" && client != nil {
			digits, found, err := client.ImdbOf(ctx, id)
			if err != nil {
				return err
			}
			if found {
				imdb = "tt" + digits
			} else {
				notice = chToast("error", "No IMDb id on TMDB", "Enter it by hand.")
			}
		}
		return nil
	}()
	if err != nil {
		text, kind := mustClassify(err)
		if kind != tmdbError {
			panic(err)
		}
		notice = chToast("error", "TMDB failed", text)
	}
	form := ChannelForm{IMDb: imdb, Name: strings.TrimSpace(name), TmdbID: tmdbID, Pattern: pattern, Backfill: true}
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
	ctx := r.Context()
	var tmdb *int64
	if trimmed := strings.TrimSpace(tmdbID); isDigits(trimmed) {
		if id, ok := parseInt(trimmed); ok {
			tmdb = &id
		}
	}
	set := s.settings(ctx)
	row, err := set.AddChannel(ctx, s.deps.DB.Write, name, imdb, pattern, caseSensitive, tmdb)
	if err != nil {
		text, kind := mustClassify(err)
		if kind != channelError {
			panic(err)
		}
		s.chPage(w, r, chToast("error", "Not saved", text), http.StatusUnprocessableEntity, "", &form, nil, false)
		return
	}
	if !backfill {
		s.chPage(w, r, chToast("success", "Watching "+row.Name, "The first poll only records what is already out; later matches are downloaded."), http.StatusOK, "", nil, nil, false)
		return
	}
	out, err := media.Poll(ctx, s.deps.DB.Write, s.deps.Env, set, media.PollOptions{Now: s.now, Only: &row.ID, Force: true})
	if err != nil {
		text, kind := mustClassify(err)
		if kind != backendError {
			panic(err)
		}
		s.chPage(w, r, chToast("error", "Watching "+row.Name, "Nothing downloaded: "+text), http.StatusOK, "", nil, nil, false)
		return
	}
	s.chPage(w, r, chPolled("Watching "+row.Name, "no matching releases yet", out), http.StatusOK, "", nil, nil, false)
}

func chPolled(title, idle string, out media.PollResult) *tpl.Toast {
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
	imdb := web.FormString(values, "imdb", "")
	pattern := web.FormString(values, "pattern", "")
	caseSensitive := web.FormString(values, "case_sensitive", "") != ""
	view := PreviewView{}
	digits, ok := media.IMDBDigits(imdb)
	if !ok {
		view.Error = "Pick a show or enter an IMDb id first."
	} else if rows, err := s.chReleases(r, digits, pattern, caseSensitive); err != nil {
		view.Error = err.Error()
	} else {
		releases := make([]ReleaseView, len(rows))
		for i, row := range rows {
			releases[i] = ReleaseView{Title: row.Title, Match: row.Match, SizeBytes: row.SizeBytes}
			if row.Match {
				view.Matched++
			}
		}
		view.Releases = releases
	}
	f, err := s.deps.Render.Frame(r, "", view)
	if err != nil {
		panic(err)
	}
	if err := s.deps.Render.Fragment(w, r, http.StatusOK, "_media_preview.gohtml", f); err != nil {
		panic(err)
	}
}

func (s *service) chReleases(r *http.Request, digits, pattern string, caseSensitive bool) ([]media.PreviewRow, error) {
	set := s.settings(r.Context())
	releases, err := set.FetchReleases(r.Context(), digits)
	if err != nil {
		return nil, chExpected(err)
	}
	if strings.TrimSpace(pattern) == "" {
		rows := make([]media.PreviewRow, len(releases))
		for i, release := range releases {
			rows[i] = media.PreviewRow{Release: release}
		}
		return rows, nil
	}
	rows, err := set.Preview(releases, pattern, caseSensitive)
	return rows, chExpected(err)
}

func chExpected(err error) error {
	if err == nil {
		return nil
	}
	if _, kind, ok := classifyError(err); !ok || kind != channelError {
		panic(err)
	}
	return err
}

func (s *service) channelDelete(w http.ResponseWriter, r *http.Request) {
	raw := web.PathParam(r, "channel_id")
	id, ok := laxInt64(raw)
	if !ok {
		web.Unprocessable(w, web.BadInt("path", "channel_id", raw))
		return
	}
	row, err := media.DeleteChannel(r.Context(), s.deps.DB.Write, id)
	if err != nil {
		var refused media.ChannelError
		if !errors.As(err, &refused) {
			panic(err)
		}
		s.deps.Error(w, r, http.StatusNotFound, refused.Error())
		return
	}
	s.chPage(w, r, chToast("success", "Removed "+row.Name, ""), http.StatusOK, "", nil, nil, false)
}

func (s *service) channelPoll(w http.ResponseWriter, r *http.Request) {
	settings := s.settings(r.Context())
	out, err := media.Poll(r.Context(), s.deps.DB.Write, s.deps.Env, settings, media.PollOptions{Now: s.now})
	if err != nil {
		text, kind := mustClassify(err)
		if kind != backendError {
			panic(err)
		}
		s.chPage(w, r, chToast("error", "Not polled", text), http.StatusUnprocessableEntity, "", nil, nil, false)
		return
	}
	s.chPage(w, r, chPolled("Polled", "nothing new", out), http.StatusOK, "", nil, nil, false)
}
