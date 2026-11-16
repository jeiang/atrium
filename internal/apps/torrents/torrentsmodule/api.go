package torrentsmodule

import (
	"context"
	"reflect"
	"sort"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	torrentsapp "github.com/jeiang/atrium/internal/apps/torrents/application"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/web"
)

type TorrentsPageInput struct {
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}
type MediaTorrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Tags     string  `json:"tags"`
	Progress float64 `json:"progress"`
	Size     *int64  `json:"size"`
}
type MediaTorrents struct {
	Items       []MediaTorrent `json:"items"`
	Next        *string        `json:"next,omitempty"`
	Unavailable bool           `json:"unavailable"`
}
type TorrentsHashInput struct {
	Hash   string     `path:"hash" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}
type MediaFiles struct {
	Items []contract.TorrentFile `json:"items"`
	Next  *string                `json:"next,omitempty"`
}
type MediaAddInput struct {
	Magnet  string               `json:"magnet"`
	Torrent *torrentsapp.Torrent `json:"-"`
}
type MediaAdded struct {
	Hash string `json:"hash"`
}
type MediaSelectInput struct {
	Hash   string `path:"hash" json:"-"`
	Index  int64  `json:"index"`
	Wanted bool   `json:"wanted"`
}
type TorrentControlInput struct {
	Hash        string `path:"hash" json:"-"`
	Action      string `json:"action"`
	DeleteFiles bool   `json:"delete_files,omitempty"`
}
type TorrentControlled struct {
	Hash   string `json:"hash"`
	Action string `json:"action"`
}

func shape[T any](area string) application.APIShape {
	return application.APIShape{Area: application.Area(area), Response: reflect.TypeFor[T]()}
}

func read[A, B any](in application.Read[A], out B) application.Read[B] {
	return application.Read[B]{Revision: in.Revision, Data: out}
}

func change[A, B any](in application.Change[A], out B) application.Change[B] {
	return application.Change[B]{Data: out, AuditID: in.AuditID, EffectIDs: in.EffectIDs}
}

func cursorError() error {
	return &application.Error{Code: application.Invalid, Message: "Invalid cursor position."}
}

func routes(d *web.Deps, s *torrentsapp.Service) []application.Binding {
	return []application.Binding{
		api.Query(d, torrentsapp.DescribeDownloads(), shape[MediaTorrents]("media"), "GET /media/torrents", func(ctx context.Context, in TorrentsPageInput) (application.Read[MediaTorrents], error) {
			r, err := s.Downloads(ctx, torrentsapp.DownloadsInput{})
			if err != nil {
				return application.Read[MediaTorrents]{}, err
			}
			sort.Slice(r.Data.Torrents, func(i, j int) bool { return r.Data.Torrents[i].Hash < r.Data.Torrents[j].Hash })
			last := ""
			if in.Cursor.Set() {
				if err := in.Cursor.Last(&last); err != nil {
					return application.Read[MediaTorrents]{}, cursorError()
				}
			}
			out := MediaTorrents{Items: []MediaTorrent{}, Unavailable: r.Data.TorrentsError != nil}
			for _, t := range r.Data.Torrents {
				if t.Hash <= last {
					continue
				}
				if len(out.Items) == int(in.Limit) {
					out.Next = in.Cursor.Next(out.Items[len(out.Items)-1].Hash)
					break
				}
				out.Items = append(out.Items, MediaTorrent{Hash: t.Hash, Name: t.Name, State: t.State, Tags: t.Tags, Progress: t.Progress, Size: t.Size})
			}
			return read(r, out), nil
		}),
		api.Query(d, torrentsapp.DescribeTorrentFiles(), shape[MediaFiles]("media"), "GET /media/torrents/{hash}/files", func(ctx context.Context, in TorrentsHashInput) (application.Read[MediaFiles], error) {
			r, e := s.TorrentFiles(ctx, torrentsapp.TorrentFilesInput{Hash: in.Hash})
			out := MediaFiles{Items: []contract.TorrentFile{}}
			if e != nil {
				return read(r, out), e
			}
			sort.Slice(r.Data.Files, func(i, j int) bool { return r.Data.Files[i].Index < r.Data.Files[j].Index })
			last := -1
			if in.Cursor.Set() {
				if err := in.Cursor.Last(&last); err != nil {
					return read(r, out), cursorError()
				}
			}
			for _, f := range r.Data.Files {
				if f.Index <= last {
					continue
				}
				if len(out.Items) == int(in.Limit) {
					out.Next = in.Cursor.Next(out.Items[len(out.Items)-1].Index)
					break
				}
				out.Items = append(out.Items, f)
			}
			return read(r, out), nil
		}),
		api.Command(d, torrentsapp.DescribeAdd(), addShape(), "POST /media/torrents", func(ctx context.Context, in MediaAddInput) (application.Change[MediaAdded], error) {
			input := torrentsapp.AddInput{Media: true, Torrent: in.Torrent}
			if in.Magnet != "" {
				input.Magnet = &torrentsapp.Magnet{URI: in.Magnet}
			}
			r, e := s.Add(ctx, input)
			return change(r, MediaAdded{Hash: r.Data.Hash}), e
		}),
		api.Command(d, torrentsapp.DescribeSelectFiles(), shape[MediaAdded]("media"), "PUT /media/torrents/{hash}/files", func(ctx context.Context, in MediaSelectInput) (application.Change[MediaAdded], error) {
			r, e := s.SelectFiles(ctx, torrentsapp.SelectFilesInput{Hash: in.Hash, Index: in.Index, Wanted: in.Wanted})
			return change(r, MediaAdded{Hash: r.Data.Hash}), e
		}),
		api.Command(d, torrentsapp.DescribeControl(), shape[TorrentControlled]("torrents"), "POST /torrents/{hash}/control", func(ctx context.Context, in TorrentControlInput) (application.Change[TorrentControlled], error) {
			r, e := s.Control(ctx, torrentsapp.ControlInput{Hash: in.Hash, Action: contract.Action(in.Action), DeleteFiles: in.DeleteFiles})
			return change(r, TorrentControlled{Hash: r.Data.Hash, Action: string(r.Data.Action)}), e
		}),
	}
}
