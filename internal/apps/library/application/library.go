package application

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/library/agentclient"
	"github.com/jeiang/atrium/internal/apps/library/mediaagent/protocol"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/logx"
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
	Movies    []library.Entry
	Shows     []library.Entry
	Results   []Found
	Error     string
	Catalogue CatalogueStatus
}

type CatalogueStatus struct {
	State      string
	Problem    string
	ObservedAt time.Time
	Generation int64
}

type Found struct {
	library.SearchResult
	VPNURLs []string
}

type LibraryDetailInput struct {
	Kind string
	Name string
}

type FileLink struct {
	library.File
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
	Entry     library.Entry
	Files     []FileLink
	Seasons   []SeasonLinks
	LinkTTL   int64
	Catalogue CatalogueStatus
}

type ArtInput struct {
	Kind   string
	Name   string
	Season int64
	Rel    string
}

type FileInput struct {
	Rel             string
	Exp             string
	Sig             string
	Method          string
	Range           string
	IfRange         string
	IfNoneMatch     string
	IfModifiedSince string
}

type FileStream struct {
	io.ReadSeekCloser
	Name        string
	ModTime     time.Time
	ContentType string
}

type FileRelay struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

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
	return application.Describe[FileInput, *FileRelay](FileName, application.Query, []application.Authority{application.PublicCapability, application.Operator}, application.NoEffect, false)
}

func (s *Service) snapshot(ctx context.Context, set library.Settings) (library.Snapshot, error) {
	client, err := set.Agent(protocol.CapabilityFile)
	if err != nil {
		if errors.As(err, new(config.NotConfigured)) {
			return library.Snapshot{}, err
		}
		return library.Snapshot{}, &library.LibraryUnavailable{Cause: err}
	}
	return s.d.Catalogue.Current(ctx, func(ctx context.Context) error { return s.d.Catalogue.Refresh(ctx, client) })
}

func catalogueStatus(snap library.Snapshot) CatalogueStatus {
	out := CatalogueStatus{State: "current", ObservedAt: snap.Observed, Generation: snap.Generation}
	if snap.Stale != nil {
		out.State, out.Problem = "stale", library.CatalogueProblem(snap.Stale)
	}
	return out
}

func (s *Service) Library(ctx context.Context, in LibraryInput) (application.Read[Library], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[Library]{}, err
	}
	out := Library{Movies: []library.Entry{}, Shows: []library.Entry{}, Results: []Found{}}
	snap, err := s.snapshot(ctx, set)
	if err != nil {
		if strings.TrimSpace(in.Query) != "" || !errors.As(err, new(config.NotConfigured)) {
			return application.Read[Library]{}, classify(err)
		}
		out.Error = err.Error()
		return s.libraryRead(ctx, in, out)
	}
	out.Catalogue = catalogueStatus(snap)
	if strings.TrimSpace(in.Query) == "" {
		out.Movies, out.Shows = set.MovieEntries(snap), set.ShowEntries(snap)
		return s.libraryRead(ctx, in, out)
	}
	for _, r := range set.Search(snap, in.Query, in.Limit) {
		urls := make([]string, len(r.Files))
		for i, f := range r.Files {
			urls[i] = set.VPNURL(f)
		}
		out.Results = append(out.Results, Found{SearchResult: r, VPNURLs: urls})
	}
	return s.libraryRead(ctx, in, out)
}

func (s *Service) libraryRead(ctx context.Context, in LibraryInput, out Library) (application.Read[Library], error) {
	stable := out
	stable.Catalogue.ObservedAt = time.Time{}
	rev, err := s.revision(ctx, "media.library", in, stable)
	if err != nil {
		return application.Read[Library]{}, err
	}
	return application.Read[Library]{Revision: rev, Data: out}, nil
}

func (s *Service) link(ctx context.Context, set library.Settings, rel string) (FileLink, error) {
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
	snap, err := s.snapshot(ctx, set)
	if err != nil {
		if errors.As(err, new(config.NotConfigured)) {
			return application.Read[LibraryDetail]{}, notFound("not found")
		}
		return application.Read[LibraryDetail]{}, classify(err)
	}
	out := LibraryDetail{LinkTTL: set.LinkTTL, Catalogue: catalogueStatus(snap)}
	var content any
	switch in.Kind {
	case library.KindMovie:
		found, err := set.Movie(snap, in.Name)
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
	case library.KindShow:
		found, err := set.Show(snap, in.Name)
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
	rev, err := s.revision(ctx, "media.library-detail", in, []any{content, out.LinkTTL, snap.Generation, out.Catalogue.State, out.Catalogue.Problem})
	if err != nil {
		return application.Read[LibraryDetail]{}, err
	}
	return application.Read[LibraryDetail]{Revision: rev, Data: out}, nil
}

func relay(resp *http.Response) *FileRelay {
	h := make(http.Header)
	for _, name := range []string{"Accept-Ranges", "Content-Range", "Content-Length", "Content-Type", "ETag", "Last-Modified", "Content-Disposition"} {
		if values := resp.Header.Values(name); len(values) > 0 {
			h[name] = values
		}
	}
	return &FileRelay{Status: resp.StatusCode, Header: h, Body: resp.Body}
}

func (s *Service) File(ctx context.Context, in FileInput) (*FileRelay, error) {
	if (in.Method != http.MethodGet && in.Method != http.MethodHead) || !s.d.Links.Verify(ctx, in.Rel, in.Exp, in.Sig, s.d.Now()) {
		return nil, &application.Error{Code: application.Forbidden, Message: "the link is invalid or has expired"}
	}
	root, rel, ok := library.SplitRel(in.Rel)
	if !ok {
		return nil, notFound("not found")
	}
	set, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	client, err := set.Agent(protocol.CapabilityStream)
	if err != nil {
		return nil, classify(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	resp, err := client.Stream(ctx, agentclient.StreamRequest{
		Path: protocol.Path{Root: root, Rel: rel}, Method: in.Method,
		Range: in.Range, IfRange: in.IfRange, IfNoneMatch: in.IfNoneMatch, IfModifiedSince: in.IfModifiedSince,
	})
	if err != nil {
		cancel()
		return nil, &application.Error{Code: application.Unavailable, Message: "the media agent is unavailable", Cause: err}
	}
	out := relay(resp)
	idle := time.AfterFunc(streamIdle, cancel)
	idle.Stop()
	out.Body = &idleBody{body: resp.Body, idle: idle, cancel: cancel}
	return out, nil
}

const streamIdle = time.Minute

type idleBody struct {
	body   io.ReadCloser
	idle   *time.Timer
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	b.idle.Reset(streamIdle)
	n, err := b.body.Read(p)
	b.idle.Stop()
	return n, err
}

func (b *idleBody) Close() error {
	b.idle.Stop()
	b.cancel()
	return b.body.Close()
}

func (s *Service) RefreshCatalogue(ctx context.Context) error {
	defer s.d.Catalogue.Bind(ctx)()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if set, err := s.settings(ctx); err == nil {
			if client, err := set.Agent(protocol.CapabilityFile); err == nil {
				err := s.d.Catalogue.Refresh(ctx, client)
				if _, held := connections.HeldUntil(err); err != nil && !held && ctx.Err() == nil {
					logx.Logger("media").Warn("media catalogue refresh failed", logx.Err, err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		case <-s.d.Catalogue.Nudges():
		}
	}
}
