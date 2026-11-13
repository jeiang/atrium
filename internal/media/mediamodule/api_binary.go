package mediamodule

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
)

type MediaContentInput struct {
	ID              string `path:"file_id" json:"-"`
	Range           string `header:"Range" json:"-"`
	IfRange         string `header:"If-Range" json:"-"`
	IfNoneMatch     string `header:"If-None-Match" json:"-"`
	IfModifiedSince string `header:"If-Modified-Since" json:"-"`
}

type MediaArtInput struct {
	Kind   string `path:"kind" json:"-" api:"enum=movie|show|season"`
	ID     string `path:"id" json:"-"`
	Season *int64 `query:"season" json:"-" api:"min=0"`
}

type MediaInspectionInput struct {
	ID int64 `path:"effect_id" json:"-" api:"min=1"`
}
type MediaInspectionStep struct {
	Index int    `json:"index"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}
type MediaInspection struct {
	EffectID     int64                 `json:"effect_id"`
	State        string                `json:"state"`
	ReceiptState string                `json:"receipt_state"`
	Steps        []MediaInspectionStep `json:"steps"`
}

func mediaBinaryShape() application.APIShape {
	shape := mediaShape[api.Binary]()
	shape.Body = application.BodyBinary
	return shape
}

func mediaRelay(relay *mediaapp.FileRelay, err error) (application.Read[api.Binary], error) {
	if err != nil {
		return application.Read[api.Binary]{}, err
	}
	if relay.Status >= 500 {
		_ = relay.Body.Close()
		return application.Read[api.Binary]{}, mediaUnavailable()
	}
	if relay.Status != 200 && relay.Status != 206 && relay.Status != 304 && relay.Status != 416 {
		_ = relay.Body.Close()
		return application.Read[api.Binary]{}, mediaMissing()
	}
	if relay.Status == 416 {
		relay.Header.Set("Content-Length", "0")
	}
	return application.Read[api.Binary]{Data: api.Binary{Status: relay.Status, Header: relay.Header, Body: relay.Body}}, nil
}

func mediaBinaryRoutes(d *web.Deps, s *mediaapp.Service) []application.Binding {
	routes := []application.Binding{}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		shape := mediaBinaryShape()
		if method == http.MethodHead {
			shape.ID = "media.file.head"
		}
		routes = append(routes, api.Query(d, mediaapp.DescribeFile(), shape, method+" /media/files/{file_id}/content", func(ctx context.Context, in MediaContentInput) (application.Read[api.Binary], error) {
			rel, err := mediaDecode(in.ID)
			if err != nil {
				return application.Read[api.Binary]{}, err
			}
			return mediaRelay(s.OperatorFile(ctx, mediaapp.FileInput{Rel: rel, Method: method, Range: in.Range, IfRange: in.IfRange, IfNoneMatch: in.IfNoneMatch, IfModifiedSince: in.IfModifiedSince}))
		}))
	}
	routes = append(routes, api.Query(d, mediaapp.DescribeThumbnail(), mediaBinaryShape(), "GET /media/files/{file_id}/thumbnail", func(ctx context.Context, in MediaContentInput) (application.Read[api.Binary], error) {
		rel, err := mediaDecode(in.ID)
		if err != nil {
			return application.Read[api.Binary]{}, err
		}
		return mediaRelay(s.Thumbnail(ctx, mediaapp.ThumbnailInput{Rel: rel, Method: http.MethodGet, IfNoneMatch: in.IfNoneMatch, IfModifiedSince: in.IfModifiedSince}))
	}), api.Query(d, mediaapp.DescribeArt(), mediaBinaryShape(), "GET /media/art/{kind}/{id}", func(ctx context.Context, in MediaArtInput) (application.Read[api.Binary], error) {
		name, err := mediaDecode(in.ID)
		if err != nil {
			return application.Read[api.Binary]{}, err
		}
		if (in.Kind == media.KindSeason) != (in.Season != nil) {
			return application.Read[api.Binary]{}, &application.Error{Code: application.Invalid, Message: "season is required for season art and only for season art.", Fields: []application.Field{{Name: "season", Code: "invalid", Message: "Input should be given only when kind is season"}}}
		}
		var season int64
		if in.Season != nil {
			season = *in.Season
		}
		stream, err := s.Art(ctx, mediaapp.ArtInput{Kind: in.Kind, Name: name, Season: season})
		if err != nil {
			return application.Read[api.Binary]{}, err
		}
		if stream == nil {
			return application.Read[api.Binary]{}, mediaMissing()
		}
		return application.Read[api.Binary]{Data: api.Binary{Name: stream.Name, MediaType: stream.ContentType, ModTime: stream.ModTime, File: stream.ReadSeekCloser}}, nil
	}), api.Query(d, mediaapp.DescribeFilingInspect(), mediaShape[MediaInspection](), "GET /media/filing-effects/{effect_id}", func(ctx context.Context, in MediaInspectionInput) (application.Read[MediaInspection], error) {
		result, err := s.InspectFiling(ctx, mediaapp.InspectFilingInput{EffectID: in.ID})
		if err != nil {
			return application.Read[MediaInspection]{}, err
		}
		out := MediaInspection{EffectID: result.Data.EffectID, State: result.Data.State, ReceiptState: result.Data.Receipt.Result.State, Steps: []MediaInspectionStep{}}
		for _, step := range result.Data.Receipt.Result.Steps {
			out.Steps = append(out.Steps, MediaInspectionStep{Index: step.Index, State: step.State, Error: step.ErrorCode})
		}
		return mediaRead(result, out), nil
	}))
	return routes
}
