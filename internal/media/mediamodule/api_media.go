package mediamodule

import (
	"context"
	"reflect"
	"sort"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
)

type MediaPageInput struct {
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}
type MediaChannels struct {
	Items []media.Channel `json:"items"`
	Next  *string         `json:"next,omitempty"`
}
type MediaChannelInput struct {
	Name          string `json:"name"`
	IMDB          string `json:"imdb,omitempty"`
	TMDBID        *int64 `json:"tmdb_id,omitempty"`
	Pattern       string `json:"pattern,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
	Backfill      bool   `json:"backfill,omitempty"`
}
type MediaChannelID struct {
	ID int64 `path:"channel_id" json:"-" api:"min=1"`
}
type MediaChannelResult struct {
	Channel media.Channel `json:"channel"`
	Polled  bool          `json:"polled"`
}
type MediaPollResult struct {
	Added  int `json:"added"`
	Errors int `json:"errors"`
}
type MediaShowSearch struct {
	Query  string     `query:"q" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}
type MediaShowID struct {
	ID int64 `path:"show_id" json:"-" api:"min=1"`
}
type MediaShow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Year string `json:"year"`
}
type MediaShows struct {
	Items []MediaShow `json:"items"`
	Next  *string     `json:"next,omitempty"`
}
type MediaShowResult struct {
	IMDB  string `json:"imdb"`
	Found bool   `json:"found"`
}
type MediaPreviewInput struct {
	IMDB          string     `query:"imdb" json:"-"`
	Pattern       string     `query:"pattern" json:"-"`
	CaseSensitive bool       `query:"case_sensitive" json:"-"`
	Limit         api.Limit  `query:"limit" json:"-"`
	Cursor        api.Cursor `query:"cursor" json:"-"`
}
type MediaRelease struct {
	Title string `json:"title"`
	Match bool   `json:"match"`
}
type MediaPreview struct {
	Items   []MediaRelease `json:"items"`
	Matched int            `json:"matched"`
	Next    *string        `json:"next,omitempty"`
}

func mediaShape[T any]() application.APIShape {
	return application.APIShape{Area: application.Area("media"), Response: reflect.TypeFor[T]()}
}
func mediaRead[A, B any](in application.Read[A], out B) application.Read[B] {
	return application.Read[B]{Revision: in.Revision, Data: out}
}
func mediaChange[A, B any](in application.Change[A], out B) application.Change[B] {
	return application.Change[B]{Data: out, AuditID: in.AuditID, EffectIDs: in.EffectIDs}
}
func mediaCursorError() error {
	return &application.Error{Code: application.Invalid, Message: "Invalid cursor position."}
}

func mediaRoutes(d *web.Deps, s *mediaapp.Service) []application.Binding {
	routes := []application.Binding{
		api.Query(d, mediaapp.DescribeChannels(), mediaShape[MediaChannels](), "GET /media/channels", func(ctx context.Context, in MediaPageInput) (application.Read[MediaChannels], error) {
			r, e := s.Channels(ctx, mediaapp.ChannelsInput{})
			if e != nil {
				return application.Read[MediaChannels]{}, e
			}
			sort.Slice(r.Data.Channels, func(i, j int) bool { return r.Data.Channels[i].ID < r.Data.Channels[j].ID })
			var last int64
			if in.Cursor.Set() {
				if e := in.Cursor.Last(&last); e != nil {
					return application.Read[MediaChannels]{}, mediaCursorError()
				}
			}
			out := MediaChannels{Items: []media.Channel{}}
			for _, c := range r.Data.Channels {
				if c.ID <= last {
					continue
				}
				if len(out.Items) == int(in.Limit) {
					out.Next = in.Cursor.Next(out.Items[len(out.Items)-1].ID)
					break
				}
				out.Items = append(out.Items, c)
			}
			return mediaRead(r, out), nil
		}),
		api.Command(d, mediaapp.DescribeChannelAdd(), mediaShape[MediaChannelResult](), "POST /media/channels", func(ctx context.Context, in MediaChannelInput) (application.Change[MediaChannelResult], error) {
			r, e := s.ChannelAdd(ctx, mediaapp.ChannelAddInput{Name: in.Name, IMDB: in.IMDB, TMDBID: in.TMDBID, Pattern: in.Pattern, CaseSensitive: in.CaseSensitive, Backfill: in.Backfill})
			return mediaChange(r, MediaChannelResult{Channel: r.Data.Channel, Polled: r.Data.Polled}), e
		}),
		api.Command(d, mediaapp.DescribeChannelRemove(), application.APIShape{Area: "media", Response: reflect.TypeFor[MediaChannelResult](), Body: application.BodyNone}, "DELETE /media/channels/{channel_id}", func(ctx context.Context, in MediaChannelID) (application.Change[MediaChannelResult], error) {
			r, e := s.ChannelRemove(ctx, mediaapp.ChannelRemoveInput{ID: in.ID})
			return mediaChange(r, MediaChannelResult{Channel: r.Data.Channel}), e
		}),
		api.Command(d, mediaapp.DescribePoll(), mediaShape[MediaPollResult](), "POST /media/channels/poll", func(ctx context.Context, in struct{}) (application.Change[MediaPollResult], error) {
			r, e := s.Poll(ctx, mediaapp.PollInput{})
			return mediaChange(r, MediaPollResult{Added: len(r.Data.Result.Added), Errors: len(r.Data.Result.Errors)}), e
		}),
		api.Query(d, mediaapp.DescribeShowSearch(), mediaShape[MediaShows](), "GET /media/shows", func(ctx context.Context, in MediaShowSearch) (application.Read[MediaShows], error) {
			r, e := s.ShowSearch(ctx, mediaapp.ShowSearchInput{Query: in.Query})
			out := MediaShows{Items: []MediaShow{}}
			if e != nil {
				return mediaRead(r, out), e
			}
			sort.Slice(r.Data, func(i, j int) bool { return r.Data[i].TmdbID < r.Data[j].TmdbID })
			var last int64
			if in.Cursor.Set() {
				if err := in.Cursor.Last(&last); err != nil {
					return mediaRead(r, out), mediaCursorError()
				}
			}
			for _, v := range r.Data {
				if v.TmdbID <= last {
					continue
				}
				if len(out.Items) == int(in.Limit) {
					out.Next = in.Cursor.Next(out.Items[len(out.Items)-1].ID)
					break
				}
				out.Items = append(out.Items, MediaShow{ID: v.TmdbID, Name: v.Name, Year: v.Year})
			}
			return mediaRead(r, out), e
		}),
		api.Query(d, mediaapp.DescribeShowLookup(), mediaShape[MediaShowResult](), "GET /media/shows/{show_id}", func(ctx context.Context, in MediaShowID) (application.Read[MediaShowResult], error) {
			r, e := s.ShowLookup(ctx, mediaapp.ShowLookupInput{TMDBID: in.ID})
			return mediaRead(r, MediaShowResult{IMDB: r.Data.IMDB, Found: r.Data.Found}), e
		}),
		api.Query(d, mediaapp.DescribeReleasePreview(), mediaShape[MediaPreview](), "GET /media/channels/preview", func(ctx context.Context, in MediaPreviewInput) (application.Read[MediaPreview], error) {
			r, e := s.ReleasePreview(ctx, mediaapp.ReleasePreviewInput{IMDB: in.IMDB, Pattern: in.Pattern, CaseSensitive: in.CaseSensitive})
			out := MediaPreview{Items: []MediaRelease{}, Matched: r.Data.Matched}
			if e != nil {
				return mediaRead(r, out), e
			}
			sort.Slice(r.Data.Releases, func(i, j int) bool { return r.Data.Releases[i].Hash < r.Data.Releases[j].Hash })
			last := ""
			if in.Cursor.Set() {
				if err := in.Cursor.Last(&last); err != nil {
					return mediaRead(r, out), mediaCursorError()
				}
			}
			for _, v := range r.Data.Releases {
				if v.Hash <= last {
					continue
				}
				if len(out.Items) == int(in.Limit) {
					out.Next = in.Cursor.Next(last)
					break
				}
				out.Items = append(out.Items, MediaRelease{Title: v.Title, Match: v.Match})
				last = v.Hash
			}
			return mediaRead(r, out), e
		}),
	}
	return append(append(append(routes, mediaLibraryRoutes(d, s)...), mediaFilingRoutes(d, s)...), mediaBinaryRoutes(d, s)...)
}
