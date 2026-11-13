package mediamodule

import (
	"context"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
)

type MediaLibraryInput struct {
	Query  string     `query:"q" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}
type MediaLibraryEntry struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Year  string `json:"year"`
}
type MediaCatalogue struct {
	State      string    `json:"state"`
	Stale      bool      `json:"stale"`
	ObservedAt time.Time `json:"observed_at"`
	Generation int64     `json:"generation"`
}
type MediaLibrary struct {
	Items     []MediaLibraryEntry `json:"items"`
	Next      *string             `json:"next,omitempty"`
	Catalogue MediaCatalogue      `json:"catalogue"`
}
type MediaLibraryID struct {
	Kind   string     `path:"kind" json:"-" api:"enum=movie|show"`
	ID     string     `path:"id" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}
type MediaLibraryFile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Season  *int   `json:"season,omitempty"`
	Episode *int   `json:"episode,omitempty"`
}
type MediaLibraryDetail struct {
	Entry     MediaLibraryEntry  `json:"entry"`
	Items     []MediaLibraryFile `json:"items"`
	Next      *string            `json:"next,omitempty"`
	Catalogue MediaCatalogue     `json:"catalogue"`
}
type MediaFileID struct {
	ID string `path:"file_id" json:"-"`
}
type MediaFileLink struct {
	Public    string         `json:"public,omitempty"`
	VPN       string         `json:"vpn,omitempty"`
	TTL       int64          `json:"ttl_seconds"`
	Catalogue MediaCatalogue `json:"catalogue"`
}
type MediaFilingQuery struct {
	Hash  string `path:"hash" json:"-"`
	Kind  string `query:"kind" json:"-"`
	Title string `query:"title" json:"-"`
	Year  string `query:"year" json:"-"`
}
type MediaFilingEpisode struct {
	Season  string `json:"season"`
	Episode string `json:"episode"`
}
type MediaFilingInput struct {
	Hash     string               `path:"hash" json:"-"`
	Action   string               `json:"action"`
	Kind     string               `json:"kind"`
	Title    string               `json:"title"`
	Year     string               `json:"year"`
	Episodes []MediaFilingEpisode `json:"episodes,omitempty"`
}
type MediaFilingHash struct {
	Hash string `path:"hash" json:"-"`
}
type MediaFilingPreview struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Year  string `json:"year"`
	Files int    `json:"files"`
	Ready bool   `json:"ready"`
	Error string `json:"error,omitempty"`
}
type MediaFiled struct {
	ID     string `json:"id"`
	Placed int    `json:"placed"`
}
type MediaSwept struct {
	Selected int `json:"selected"`
	Removed  int `json:"removed"`
	Errors   int `json:"errors"`
}

func mediaID(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func mediaMissing() error {
	return &application.Error{Code: application.NotFound, Message: "Not found."}
}

func mediaUnavailable() error {
	return &application.Error{Code: application.Unavailable, Message: "The media library catalogue is unavailable."}
}

func mediaDecode(s string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 || strings.ContainsAny(string(b), "\\\x00") {
		return "", mediaMissing()
	}
	return string(b), nil
}

func mediaCatalogue(c mediaapp.CatalogueStatus) MediaCatalogue {
	return MediaCatalogue{State: c.State, Stale: c.State == "stale", ObservedAt: c.ObservedAt, Generation: c.Generation}
}

func mediaPage[T any](items []T, key func(T) string, in api.Cursor, limit api.Limit) ([]T, *string, error) {
	sort.Slice(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
	last := ""
	if in.Set() {
		if err := in.Last(&last); err != nil {
			return nil, nil, mediaCursorError()
		}
	}
	out := []T{}
	for _, v := range items {
		if key(v) <= last {
			continue
		}
		if len(out) == int(limit) {
			return out, in.Next(key(out[len(out)-1])), nil
		}
		out = append(out, v)
	}
	return out, nil, nil
}

func mediaCatalogued(ctx context.Context, s *mediaapp.Service) error {
	r, err := s.Library(ctx, mediaapp.LibraryInput{})
	if err != nil {
		return err
	}
	if r.Data.Catalogue.State == "" {
		return mediaUnavailable()
	}
	return nil
}

func mediaDetail(ctx context.Context, s *mediaapp.Service, kind, name string) (application.Read[mediaapp.LibraryDetail], error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return application.Read[mediaapp.LibraryDetail]{}, mediaMissing()
	}
	r, err := s.LibraryDetail(ctx, mediaapp.LibraryDetailInput{Kind: kind, Name: name})
	if err != nil && application.Classify(err).Code == application.NotFound {
		if missing := mediaCatalogued(ctx, s); missing != nil {
			return application.Read[mediaapp.LibraryDetail]{}, missing
		}
	}
	return r, err
}

func mediaLibraryRoutes(d *web.Deps, s *mediaapp.Service) []application.Binding {
	return []application.Binding{
		api.Query(d, mediaapp.DescribeLibrary(), mediaShape[MediaLibrary](), "GET /media/library", func(ctx context.Context, in MediaLibraryInput) (application.Read[MediaLibrary], error) {
			r, err := s.Library(ctx, mediaapp.LibraryInput{Query: in.Query, Limit: int(^uint(0) >> 1)})
			out := MediaLibrary{Items: []MediaLibraryEntry{}, Catalogue: mediaCatalogue(r.Data.Catalogue)}
			if err != nil {
				return mediaRead(r, out), err
			}
			if r.Data.Catalogue.State == "" {
				return mediaRead(r, out), mediaUnavailable()
			}
			entries := []MediaLibraryEntry{}
			for _, v := range append(append([]media.Entry{}, r.Data.Movies...), r.Data.Shows...) {
				entries = append(entries, MediaLibraryEntry{ID: mediaID(v.Dir), Kind: v.Kind, Title: v.Title, Year: v.Year})
			}
			seen := map[string]bool{}
			for _, v := range r.Data.Results {
				kind := v.Kind
				if kind == media.KindEpisode {
					kind = media.KindShow
				}
				if !seen[kind+"/"+v.Dir] {
					seen[kind+"/"+v.Dir] = true
					entries = append(entries, MediaLibraryEntry{ID: mediaID(v.Dir), Kind: kind, Title: v.Title, Year: v.Year})
				}
			}
			out.Items, out.Next, err = mediaPage(entries, func(v MediaLibraryEntry) string { return v.Kind + "/" + v.ID }, in.Cursor, in.Limit)
			return mediaRead(r, out), err
		}),
		api.Query(d, mediaapp.DescribeLibraryDetail(), mediaShape[MediaLibraryDetail](), "GET /media/library/{kind}/{id}", func(ctx context.Context, in MediaLibraryID) (application.Read[MediaLibraryDetail], error) {
			out := MediaLibraryDetail{Items: []MediaLibraryFile{}}
			name, err := mediaDecode(in.ID)
			if err != nil {
				return application.Read[MediaLibraryDetail]{}, err
			}
			r, err := mediaDetail(ctx, s, in.Kind, name)
			if err != nil {
				return mediaRead(r, out), err
			}
			out.Entry = MediaLibraryEntry{ID: in.ID, Kind: in.Kind, Title: r.Data.Entry.Title, Year: r.Data.Entry.Year}
			out.Catalogue = mediaCatalogue(r.Data.Catalogue)
			files := []MediaLibraryFile{}
			for _, f := range r.Data.Files {
				files = append(files, MediaLibraryFile{ID: mediaID(f.Rel), Name: f.Name, Size: f.Size})
			}
			for _, season := range r.Data.Seasons {
				for _, f := range season.Episodes {
					files = append(files, MediaLibraryFile{ID: mediaID(f.Rel), Name: f.Name, Size: f.Size, Season: f.Season, Episode: f.Episode})
				}
			}
			out.Items, out.Next, err = mediaPage(files, func(v MediaLibraryFile) string { return v.ID }, in.Cursor, in.Limit)
			return mediaRead(r, out), err
		}),
		api.Query(d, mediaapp.DescribeLibraryDetail(), mediaShape[MediaFileLink](), "GET /media/files/{file_id}/link", func(ctx context.Context, in MediaFileID) (application.Read[MediaFileLink], error) {
			rel, err := mediaDecode(in.ID)
			if err != nil {
				return application.Read[MediaFileLink]{}, err
			}
			root, rest, ok := media.SplitRel(rel)
			if !ok {
				return application.Read[MediaFileLink]{}, mediaMissing()
			}
			kind := media.KindMovie
			if root == media.TV {
				kind = media.KindShow
			}
			name, _, _ := strings.Cut(rest, "/")
			r, err := mediaDetail(ctx, s, kind, name)
			if err != nil {
				return mediaRead(r, MediaFileLink{}), err
			}
			files := append([]mediaapp.FileLink{}, r.Data.Files...)
			for _, season := range r.Data.Seasons {
				for _, f := range season.Episodes {
					files = append(files, f.FileLink)
				}
			}
			for _, f := range files {
				if f.Rel == rel {
					return mediaRead(r, MediaFileLink{Public: f.Public, VPN: f.VPN, TTL: r.Data.LinkTTL, Catalogue: mediaCatalogue(r.Data.Catalogue)}), nil
				}
			}
			return mediaRead(r, MediaFileLink{}), mediaMissing()
		}),
	}
}

func mediaFilingPreview(p mediaapp.FilingPreview) MediaFilingPreview {
	return MediaFilingPreview{ID: p.Item.ID, Kind: p.View.Kind, Title: p.View.Title, Year: p.View.Year, Files: len(p.Ops), Ready: p.Error == "", Error: p.Error}
}

func mediaFilingRoutes(d *web.Deps, s *mediaapp.Service) []application.Binding {
	none := func(shape application.APIShape) application.APIShape {
		shape.Body = application.BodyNone
		return shape
	}
	return []application.Binding{
		api.Query(d, mediaapp.DescribeFilingPreview(), mediaShape[MediaFilingPreview](), "GET /media/filings/{hash}", func(ctx context.Context, in MediaFilingQuery) (application.Read[MediaFilingPreview], error) {
			r, err := s.FilingPreview(ctx, mediaapp.FilingInput{ID: in.Hash, Form: map[string]string{"kind": in.Kind, "title": in.Title, "year": in.Year}})
			return mediaRead(r, mediaFilingPreview(r.Data)), err
		}),
		api.Command(d, mediaapp.DescribeFileItem(), mediaShape[MediaFiled](), "POST /media/filings/{hash}", func(ctx context.Context, in MediaFilingInput) (application.Change[MediaFiled], error) {
			if in.Action != "file" {
				return application.Change[MediaFiled]{}, &application.Error{Code: application.Invalid, Message: "Confirm the filing with action file.", Fields: []application.Field{{Name: "action", Code: "invalid", Message: "Input should be file"}}}
			}
			form := map[string]string{"kind": in.Kind, "title": in.Title, "year": in.Year}
			for i, ep := range in.Episodes {
				form["s"+strconv.Itoa(i)], form["e"+strconv.Itoa(i)] = ep.Season, ep.Episode
			}
			input := mediaapp.FilingInput{ID: in.Hash, Form: form}
			preview, err := s.FilingPreview(ctx, input)
			if err != nil {
				return application.Change[MediaFiled]{}, err
			}
			if preview.Data.Error != "" {
				return application.Change[MediaFiled]{}, &application.Error{Code: application.Invalid, Message: preview.Data.Error, Fields: []application.Field{{Name: "plan", Code: "invalid", Message: preview.Data.Error}}}
			}
			r, err := s.FileItem(ctx, input)
			return mediaChange(r, MediaFiled{ID: in.Hash, Placed: len(r.Data.Dests)}), err
		}),
		api.Command(d, mediaapp.DescribeIgnore(), none(mediaShape[MediaFiled]()), "POST /media/filings/{hash}/ignore", func(ctx context.Context, in MediaFilingHash) (application.Change[MediaFiled], error) {
			r, err := s.Ignore(ctx, mediaapp.IgnoreInput{ID: in.Hash})
			return mediaChange(r, MediaFiled{ID: r.Data.ID}), err
		}),
		api.Command(d, mediaapp.DescribeSweep(), mediaShape[MediaSwept](), "POST /media/sweep", func(ctx context.Context, _ struct{}) (application.Change[MediaSwept], error) {
			r, err := s.Sweep(ctx, mediaapp.SweepInput{})
			return mediaChange(r, MediaSwept{Selected: len(r.Data.Selected), Removed: len(r.Data.Removed), Errors: len(r.Data.Errors)}), err
		}),
	}
}
