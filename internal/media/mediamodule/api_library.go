package mediamodule

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
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
	Hash  string `path:"hash" json:"-" doc:"Torrent info hash, or Import/<name> for an import item (send the slash as %2F)."`
	Kind  string `query:"kind" json:"-" api:"enum=movie|show"`
	Title string `query:"title" json:"-"`
	Year  string `query:"year" json:"-"`
	Edits string `query:"episodes" json:"-" doc:"Episode overrides as comma-separated file:season:episode entries, where file is an id from the preview and an empty episode skips the file."`
}
type MediaFilingEpisode struct {
	File    string `json:"file"`
	Season  string `json:"season"`
	Episode string `json:"episode"`
}
type MediaFilingPlan struct {
	Hash     string               `path:"hash" json:"-" doc:"Torrent info hash, or Import/<name> for an import item (send the slash as %2F)."`
	Kind     string               `json:"kind" api:"enum=movie|show"`
	Title    string               `json:"title"`
	Year     string               `json:"year"`
	Episodes []MediaFilingEpisode `json:"episodes,omitempty"`
}
type MediaFilingInput struct {
	MediaFilingPlan
	Action string `json:"action" api:"enum=file"`
}
type MediaFilingHash struct {
	Hash string `path:"hash" json:"-" doc:"Torrent info hash, or Import/<name> for an import item (send the slash as %2F)."`
}
type MediaFilingFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Season      string `json:"season,omitempty"`
	Episode     string `json:"episode,omitempty"`
	Destination string `json:"destination,omitempty"`
}
type MediaFilingPreview struct {
	ID    string            `json:"id"`
	Kind  string            `json:"kind" api:"enum=movie|show"`
	Title string            `json:"title"`
	Year  string            `json:"year"`
	Files []MediaFilingFile `json:"files"`
	Ready bool              `json:"ready"`
	Error string            `json:"error,omitempty"`
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
	link := mediaShape[MediaFileLink]()
	link.ID, link.Uncached = "media.file-link", true
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
		api.Query(d, mediaapp.DescribeLibraryDetail(), link, "GET /media/files/{file_id}/link", func(ctx context.Context, in MediaFileID) (application.Read[MediaFileLink], error) {
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
	out := MediaFilingPreview{ID: p.Item.ID, Kind: p.View.Kind, Title: p.View.Title, Year: p.View.Year, Files: []MediaFilingFile{}, Ready: p.Error == "", Error: p.Error}
	if out.Kind == media.KindTV {
		out.Kind = media.KindShow
	}
	for _, f := range p.View.Files {
		file := MediaFilingFile{ID: mediaID(f.Rel), Name: f.Rel}
		switch {
		case p.View.Kind == media.KindTV:
			file.Season, file.Episode = f.Season, f.Episode
			if f.Dest != media.Skipped {
				file.Destination = f.Dest
			}
		case f.Rel == p.View.MovieFile:
			file.Destination = p.View.MovieDest
		}
		out.Files = append(out.Files, file)
	}
	return out
}

func mediaFilingInput(hash, kind, title, year string, episodes []MediaFilingEpisode) (mediaapp.FilingInput, error) {
	if kind == media.KindShow {
		kind = media.KindTV
	}
	in := mediaapp.FilingInput{ID: hash, Form: map[string]string{"kind": kind, "title": title, "year": year}}
	for i, ep := range episodes {
		rel, err := base64.RawURLEncoding.DecodeString(ep.File)
		if _, dup := in.Episodes[string(rel)]; err != nil || len(rel) == 0 || dup {
			name := "episodes." + strconv.Itoa(i) + ".file"
			return in, &application.Error{Code: application.Invalid, Message: "An episode must name one file from the preview.", Fields: []application.Field{{Name: name, Code: "invalid", Message: "Input should be a file id from the preview"}}}
		}
		if in.Episodes == nil {
			in.Episodes = map[string]mediaapp.FilingEpisode{}
		}
		in.Episodes[string(rel)] = mediaapp.FilingEpisode{Season: ep.Season, Episode: ep.Episode}
	}
	return in, nil
}

func mediaEdits(raw string) ([]MediaFilingEpisode, error) {
	var out []MediaFilingEpisode
	if raw == "" {
		return out, nil
	}
	for entry := range strings.SplitSeq(raw, ",") {
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return nil, &application.Error{Code: application.Invalid, Message: "The episodes parameter is not valid.", Fields: []application.Field{{Name: "episodes", Code: "invalid", Message: "Input should be file:season:episode entries"}}}
		}
		out = append(out, MediaFilingEpisode{File: parts[0], Season: parts[1], Episode: parts[2]})
	}
	return out, nil
}

func mediaJobContext(ctx context.Context, d *web.Deps) context.Context {
	return module.WithSet(config.With(ctx, d.State), module.SetFrom(d.Ctx))
}

func mediaFilingRoutes(d *web.Deps, s *mediaapp.Service) []application.Binding {
	none := func(shape application.APIShape) application.APIShape {
		shape.Body = application.BodyNone
		return shape
	}
	preview := func(ctx context.Context, in mediaapp.FilingInput, err error) (application.Read[MediaFilingPreview], error) {
		if err != nil {
			return application.Read[MediaFilingPreview]{}, err
		}
		r, err := s.FilingPreview(ctx, in)
		return mediaRead(r, mediaFilingPreview(r.Data)), err
	}
	return []application.Binding{
		api.Query(d, mediaapp.DescribeFilingPreview(), mediaShape[MediaFilingPreview](), "GET /media/filings/{hash}", func(ctx context.Context, in MediaFilingQuery) (application.Read[MediaFilingPreview], error) {
			episodes, err := mediaEdits(in.Edits)
			if err != nil {
				return application.Read[MediaFilingPreview]{}, err
			}
			input, err := mediaFilingInput(in.Hash, in.Kind, in.Title, in.Year, episodes)
			return preview(ctx, input, err)
		}),
		api.Command(d, mediaapp.DescribeFileItem(), mediaShape[MediaFiled](), "POST /media/filings/{hash}", func(ctx context.Context, in MediaFilingInput) (application.Change[MediaFiled], error) {
			input, err := mediaFilingInput(in.Hash, in.Kind, in.Title, in.Year, in.Episodes)
			if err != nil {
				return application.Change[MediaFiled]{}, err
			}
			planned, err := s.FilingPreview(ctx, input)
			if err != nil {
				return application.Change[MediaFiled]{}, err
			}
			if planned.Data.Error != "" {
				return application.Change[MediaFiled]{}, &application.Error{Code: application.Invalid, Message: planned.Data.Error, Fields: []application.Field{{Name: "plan", Code: "invalid", Message: planned.Data.Error}}}
			}
			r, err := s.FileItem(ctx, input)
			return mediaChange(r, MediaFiled{ID: in.Hash, Placed: len(r.Data.Dests)}), err
		}),
		api.Command(d, mediaapp.DescribeIgnore(), none(mediaShape[MediaFiled]()), "POST /media/filings/{hash}/ignore", func(ctx context.Context, in MediaFilingHash) (application.Change[MediaFiled], error) {
			r, err := s.Ignore(ctx, mediaapp.IgnoreInput{ID: in.Hash})
			return mediaChange(r, MediaFiled{ID: r.Data.ID}), err
		}),
		api.Command(d, mediaapp.DescribeSweep(), mediaShape[MediaSwept](), "POST /media/sweep", func(ctx context.Context, _ struct{}) (application.Change[MediaSwept], error) {
			var r application.Change[mediaapp.Swept]
			var swept error
			_, err := module.Invoke(mediaJobContext(ctx, d), d.DB, d.Cfg, module.Media+"/"+media.JobSweep, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
				r, swept = s.Sweep(ctx, mediaapp.SweepInput{})
				if missing, ok := errors.AsType[config.NotConfigured](swept); ok {
					return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
				}
				return scheduler.Result{}, swept
			})
			if _, busy := errors.AsType[*db.LockBusy](err); busy {
				err = &application.Error{Code: application.Conflict, Message: "A media sweep or channel poll is already running.", Cause: err}
			}
			if swept != nil {
				err = swept
			}
			return mediaChange(r, MediaSwept{Selected: len(r.Data.Selected), Removed: len(r.Data.Removed), Errors: len(r.Data.Errors)}), err
		}),
	}
}
