package pages

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
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

func chLastCheck(v any) *float64 {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int64:
		f = float64(x)
	default:
		return nil
	}
	return &f
}

func (s *service) chPage(w http.ResponseWriter, r *http.Request, notice *tpl.Toast, status int, q string, form *ChannelForm, results *[]SearchHit) {
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
		Status:   ChannelStatusView{LastCheck: chLastCheck(state.LastCheck), Errors: errs},
		TMDB:     set.TMDBToken != "",
		Q:        q,
		Results:  results,
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
	q := py.Strip(web.QueryValue(query, "q"))
	imdb := py.Strip(web.QueryValue(query, "imdb"))
	name := web.QueryValue(query, "name")
	tmdbID := py.Strip(web.QueryValue(query, "tmdb_id"))
	pattern := web.QueryValue(query, "pattern")
	set := s.settings(r.Context())
	client := media.NewTMDB(set.TMDBToken, set.TMDBTimeout)
	var results *[]SearchHit
	var notice *tpl.Toast
	err := func() error {
		ctx := r.Context()
		if q != "" && client != nil {
			found, err := client.SearchTV(ctx, q, set.TMDBSearchLimit)
			if err != nil {
				return err
			}
			hits := make([]SearchHit, len(found))
			for i, f := range found {
				hits[i] = SearchHit{TmdbID: f.TmdbID, Name: f.Name, Year: f.Year}
			}
			results = &hits
		}
		if id, ok := py.ParseInt(tmdbID); py.IsDigits(tmdbID) && ok && imdb == "" && client != nil {
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
		var failed media.TmdbError
		if !errors.As(err, &failed) {
			panic(err)
		}
		notice = chToast("error", "TMDB failed", failed.Error())
	}
	form := ChannelForm{IMDb: imdb, Name: py.Strip(name), TmdbID: tmdbID, Pattern: pattern, Backfill: true}
	s.chPage(w, r, notice, http.StatusOK, q, &form, results)
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
	if trimmed := py.Strip(tmdbID); py.IsDigits(trimmed) {
		if id, ok := py.ParseInt(trimmed); ok {
			tmdb = &id
		}
	}
	row, err := s.settings(ctx).AddChannel(ctx, s.deps.DB.Write, name, imdb, pattern, caseSensitive, tmdb, s.now, false)
	if err != nil {
		var refused media.ChannelError
		if !errors.As(err, &refused) {
			panic(err)
		}
		s.chPage(w, r, chToast("error", "Not saved", refused.Error()), http.StatusUnprocessableEntity, "", &form, nil)
		return
	}
	if !backfill {
		s.chPage(w, r, chToast("success", "Watching "+row.Name, "The first poll only records what is already out; later matches are downloaded."), http.StatusOK, "", nil, nil)
		return
	}
	settings := s.settings(ctx)
	qb, err := settings.Qbit()
	if err != nil {
		s.chPage(w, r, chToast("error", "Watching "+row.Name, "Nothing downloaded: "+err.Error()), http.StatusOK, "", nil, nil)
		return
	}
	out, err := media.Poll(ctx, s.deps.DB.Write, s.deps.Env, qb, settings, media.PollOptions{Now: s.now, Only: &row.ID, Force: true})
	if err != nil {
		panic(err)
	}
	s.chPage(w, r, chPolled("Watching "+row.Name, "no matching releases yet", out), http.StatusOK, "", nil, nil)
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
		view.Releases = &releases
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
	if py.Strip(pattern) == "" {
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
	var eztv media.EztvError
	var refused media.ChannelError
	if errors.As(err, &eztv) || errors.As(err, &refused) {
		return err
	}
	panic(err)
}

func (s *service) channelDelete(w http.ResponseWriter, r *http.Request) {
	raw := web.PathParam(r, "channel_id")
	id, ok := py.LaxInt64(raw)
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
	s.chPage(w, r, chToast("success", "Removed "+row.Name, ""), http.StatusOK, "", nil, nil)
}

func (s *service) channelPoll(w http.ResponseWriter, r *http.Request) {
	settings := s.settings(r.Context())
	qb, err := settings.Qbit()
	if err != nil {
		s.chPage(w, r, chToast("error", "Not polled", err.Error()), http.StatusUnprocessableEntity, "", nil, nil)
		return
	}
	out, err := media.Poll(r.Context(), s.deps.DB.Write, s.deps.Env, qb, settings, media.PollOptions{Now: s.now})
	if err != nil {
		panic(err)
	}
	s.chPage(w, r, chPolled("Polled", "nothing new", out), http.StatusOK, "", nil, nil)
}
