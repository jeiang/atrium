package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
)

const (
	DownloadsName    application.Name = "media.downloads"
	TorrentFilesName application.Name = "media.torrent-files"
	SelectFilesName  application.Name = "media.select-files"
)

type DownloadsInput struct{}

type Waiting struct {
	Item media.Item
	View media.View
}

type Downloads struct {
	Torrents      []media.Torrent
	TorrentsError *application.Error
	Queue         []Waiting
	QueueError    string
	Kinds         []string
}

func DescribeDownloads() application.Definition {
	return application.Describe[DownloadsInput, Downloads](DownloadsName, application.Query,
		[]application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}

func (s *Service) Downloads(ctx context.Context, in DownloadsInput) (application.Read[Downloads], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[Downloads]{}, err
	}
	out := Downloads{Torrents: []media.Torrent{}, Queue: []Waiting{}, Kinds: media.Kinds}
	client, err := set.Qbit()
	if err == nil {
		out.Torrents, err = client.Torrents(ctx, "", "", "")
	}
	if err != nil {
		var appErr *application.Error
		if appErr, err = known(classify(err)); err != nil {
			return application.Read[Downloads]{}, err
		}
		out.Torrents, out.TorrentsError = []media.Torrent{}, appErr
	}
	if out.QueueError, err = s.queue(ctx, set, &out); err != nil {
		return application.Read[Downloads]{}, err
	}
	rev, err := s.revision(ctx, string(DownloadsName), in, out)
	if err != nil {
		return application.Read[Downloads]{}, err
	}
	return application.Read[Downloads]{Revision: rev, Data: out}, nil
}

func (s *Service) queue(ctx context.Context, set media.Settings, out *Downloads) (string, error) {
	client, err := set.Qbit()
	if err != nil {
		return err.Error(), nil
	}
	root, err := set.NeedRoot()
	if err != nil {
		return err.Error(), nil
	}
	found, errText, err := set.Items(ctx, client, root)
	if err != nil {
		return "", err
	}
	for _, it := range found {
		out.Queue = append(out.Queue, Waiting{Item: it, View: set.Planned(set.Defaults(it, root), root)})
	}
	return errText, nil
}

func known(err error) (*application.Error, error) {
	appErr, ok := err.(*application.Error)
	if !ok {
		return nil, err
	}
	return appErr, nil
}

type TorrentFilesInput struct {
	Hash string
}

type TorrentFiles struct {
	Torrent media.Torrent
	Files   []media.TorrentFile
}

func DescribeTorrentFiles() application.Definition {
	return application.Describe[TorrentFilesInput, TorrentFiles](TorrentFilesName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) TorrentFiles(ctx context.Context, in TorrentFilesInput) (application.Read[TorrentFiles], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[TorrentFiles]{}, err
	}
	client, err := set.Qbit()
	if err != nil {
		return application.Read[TorrentFiles]{}, classify(err)
	}
	found, err := client.Torrent(ctx, in.Hash)
	if err != nil {
		return application.Read[TorrentFiles]{}, classify(err)
	}
	if found == nil {
		return application.Read[TorrentFiles]{}, notFound("unknown torrent")
	}
	files, err := client.Files(ctx, in.Hash)
	if err != nil {
		return application.Read[TorrentFiles]{}, classify(err)
	}
	out := TorrentFiles{Torrent: *found, Files: files}
	rev, err := s.revision(ctx, string(TorrentFilesName), in, out)
	if err != nil {
		return application.Read[TorrentFiles]{}, err
	}
	return application.Read[TorrentFiles]{Revision: rev, Data: out}, nil
}

type SelectFilesInput struct {
	Hash   string
	Index  int64
	Wanted bool
}

type SelectFiles struct {
	Hash string
}

func DescribeSelectFiles() application.Definition {
	return application.Describe[SelectFilesInput, SelectFiles](SelectFilesName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func (s *Service) SelectFiles(ctx context.Context, in SelectFilesInput) (application.Change[SelectFiles], error) {
	err := s.selectFiles(ctx, in)
	id, err := s.record(ctx, SelectFilesName, in.Hash, map[string]any{"index": in.Index, "wanted": in.Wanted}, err)
	if err != nil {
		return application.Change[SelectFiles]{}, err
	}
	return application.Change[SelectFiles]{Data: SelectFiles{Hash: in.Hash}, AuditID: id}, nil
}

func (s *Service) selectFiles(ctx context.Context, in SelectFilesInput) error {
	set, err := s.settings(ctx)
	if err != nil {
		return err
	}
	client, err := set.Qbit()
	if err != nil {
		return err
	}
	found, err := client.Torrent(ctx, in.Hash)
	if err != nil {
		return err
	}
	if found == nil || !found.HasTag(media.TagMedia) {
		return notFound("unknown torrent")
	}
	priority := media.PrioritySkip
	if in.Wanted {
		priority = media.PriorityNormal
	}
	return classifyWrite(set.SetPriority(ctx, s.d.Ledger, set, media.Scope{}, in.Hash, []int{int(in.Index)}, priority))
}
