package application

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
)

const (
	LibraryName       application.Name = "media.library"
	LibraryDetailName application.Name = "media.library-detail"
	ArtName           application.Name = "media.art"
	FileName          application.Name = "media.file"
)

type LibraryInput struct {
	Query string
	Limit int
}

type Library struct {
	Movies  []media.Entry
	Shows   []media.Entry
	Results []Found
	Error   string
}

type Found struct {
	media.SearchResult
	VPNURLs []string
}

type LibraryDetailInput struct {
	Kind string
	Name string
}

type FileLink struct {
	media.File
	Public string
	VPN    string
}

type EpisodeLink struct {
	FileLink
	Season  *int
	Episode *int
}

type SeasonLinks struct {
	Dir      string
	Number   *int
	Episodes []EpisodeLink
}

type LibraryDetail struct {
	Entry   media.Entry
	Files   []FileLink
	Seasons []SeasonLinks
	LinkTTL int64
}

type ArtInput struct {
	Kind   string
	Name   string
	Season int64
	Rel    string
}

type FileInput struct {
	Rel string
	Exp string
	Sig string
}

type FileStream struct {
	io.ReadSeekCloser
	Name        string
	ModTime     time.Time
	ContentType string
}

const KindThumb = "thumb"

func DescribeLibrary() application.Definition {
	return application.Describe[LibraryInput, Library](LibraryName, application.Query, []application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}

func DescribeLibraryDetail() application.Definition {
	return application.Describe[LibraryDetailInput, LibraryDetail](LibraryDetailName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeArt() application.Definition {
	return application.Describe[ArtInput, *FileStream](ArtName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeFile() application.Definition {
	return application.Describe[FileInput, *FileStream](FileName, application.Query, []application.Authority{application.PublicCapability}, application.NoEffect, false)
}

func (s *Service) Library(ctx context.Context, in LibraryInput) (application.Read[Library], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[Library]{}, err
	}
	out := Library{Movies: []media.Entry{}, Shows: []media.Entry{}, Results: []Found{}}
	root, err := set.NeedRoot()
	if err != nil {
		if strings.TrimSpace(in.Query) != "" {
			return application.Read[Library]{}, classify(err)
		}
		out.Error = err.Error()
		return s.libraryRead(ctx, in, out)
	}
	if strings.TrimSpace(in.Query) == "" {
		out.Movies, out.Shows = set.MovieEntries(root), set.ShowEntries(root)
		return s.libraryRead(ctx, in, out)
	}
	for _, r := range set.Search(root, in.Query, in.Limit) {
		urls := make([]string, len(r.Files))
		for i, f := range r.Files {
			urls[i] = set.VPNURL(f)
		}
		out.Results = append(out.Results, Found{SearchResult: r, VPNURLs: urls})
	}
	return s.libraryRead(ctx, in, out)
}

func (s *Service) libraryRead(ctx context.Context, in LibraryInput, out Library) (application.Read[Library], error) {
	rev, err := s.revision(ctx, "media.library", in, out)
	if err != nil {
		return application.Read[Library]{}, err
	}
	return application.Read[Library]{Revision: rev, Data: out}, nil
}

func (s *Service) link(ctx context.Context, set media.Settings, rel string) (FileLink, error) {
	out := FileLink{VPN: set.VPNURL(rel)}
	if set.PublicURL == "" {
		return out, nil
	}
	url, err := s.d.Links.PublicURL(ctx, set.PublicURL, rel, s.d.Now(), set.LinkTTL)
	out.Public = url
	return out, err
}

func (s *Service) LibraryDetail(ctx context.Context, in LibraryDetailInput) (application.Read[LibraryDetail], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[LibraryDetail]{}, err
	}
	root, err := set.NeedRoot()
	if err != nil {
		return application.Read[LibraryDetail]{}, notFound("not found")
	}
	out := LibraryDetail{LinkTTL: set.LinkTTL}
	var content any
	switch in.Kind {
	case media.KindMovie:
		found, err := set.Movie(root, in.Name)
		if err != nil {
			return application.Read[LibraryDetail]{}, notFound("not found")
		}
		out.Entry = found.Entry
		content = found
		out.Files = make([]FileLink, len(found.Files))
		for i, f := range found.Files {
			if out.Files[i], err = s.link(ctx, set, f.Rel); err != nil {
				return application.Read[LibraryDetail]{}, err
			}
			out.Files[i].File = f
		}
	case media.KindShow:
		found, err := set.Show(root, in.Name)
		if err != nil {
			return application.Read[LibraryDetail]{}, notFound("not found")
		}
		out.Entry = found.Entry
		content = found
		out.Seasons = make([]SeasonLinks, len(found.Seasons))
		for i, season := range found.Seasons {
			episodes := make([]EpisodeLink, len(season.Episodes))
			for j, e := range season.Episodes {
				link, err := s.link(ctx, set, e.Rel)
				if err != nil {
					return application.Read[LibraryDetail]{}, err
				}
				link.File = e.File
				episodes[j] = EpisodeLink{FileLink: link, Season: e.Season, Episode: e.Episode}
			}
			out.Seasons[i] = SeasonLinks{Dir: season.Dir, Number: season.Number, Episodes: episodes}
		}
	default:
		return application.Read[LibraryDetail]{}, notFound("not found")
	}
	rev, err := s.revision(ctx, "media.library-detail", in, []any{content, out.LinkTTL})
	if err != nil {
		return application.Read[LibraryDetail]{}, err
	}
	return application.Read[LibraryDetail]{Revision: rev, Data: out}, nil
}

func open(path, contentType string) *FileStream {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil
	}
	return &FileStream{ReadSeekCloser: f, Name: info.Name(), ModTime: info.ModTime(), ContentType: contentType}
}

func (s *Service) Art(ctx context.Context, in ArtInput) (*FileStream, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	art := media.NewArtwork(s.d.CacheDir, set.TMDB(), set.Tuning)
	var path string
	var ok bool
	switch in.Kind {
	case media.KindMovie, media.KindShow, media.KindSeason:
		if strings.Contains(in.Name, "/") {
			return nil, notFound("not found")
		}
		path, ok = art.Poster(ctx, in.Kind, in.Name, in.Season)
	case KindThumb:
		root, err := set.NeedRoot()
		if err != nil {
			return nil, nil
		}
		path, ok = art.Thumbnail(ctx, root, in.Rel)
	default:
		return nil, notFound("not found")
	}
	if !ok {
		return nil, nil
	}
	return open(path, "image/jpeg"), nil
}

var videoTypes = map[string]string{
	".mkv": "video/x-matroska",
	".mp4": "video/mp4",
	".m4v": "video/x-m4v",
	".avi": "video/x-msvideo",
	".mov": "video/quicktime",
	".ts":  "video/mp2t",
	".wmv": "video/x-ms-wmv",
}

func videoType(path string, video map[string]bool) string {
	ext := strings.ToLower(filepath.Ext(path))
	if kind, ok := videoTypes[ext]; ok && video[ext] {
		return kind
	}
	return "application/octet-stream"
}

func (s *Service) OpenFile(ctx context.Context, in FileInput) (*FileStream, error) {
	if !s.d.Links.Verify(ctx, in.Rel, in.Exp, in.Sig, s.d.Now()) {
		return nil, &application.Error{Code: application.Forbidden, Message: "the link is invalid or has expired"}
	}
	set, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	root, err := set.NeedRoot()
	if err != nil {
		return nil, notFound(err.Error())
	}
	full, ok := media.SafeRel(root, in.Rel)
	if !ok {
		return nil, notFound("not found")
	}
	stream := open(full, videoType(full, set.VideoExts))
	if stream == nil {
		return nil, notFound("not found")
	}
	return stream, nil
}
