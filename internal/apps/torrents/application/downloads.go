package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
)

const (
	DownloadsName    application.Name = "media.downloads"
	TorrentFilesName application.Name = "media.torrent-files"
	SelectFilesName  application.Name = "media.select-files"
	ControlName      application.Name = "torrents.control"
)

type DownloadsInput struct{}

type Downloads struct {
	Torrents      []contract.Torrent
	TorrentsError *application.Error
}

func DescribeDownloads() application.Definition {
	return application.Describe[DownloadsInput, Downloads](DownloadsName, application.Query,
		[]application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}

func (s *Service) Downloads(ctx context.Context, in DownloadsInput) (application.Read[Downloads], error) {
	out := Downloads{Torrents: []contract.Torrent{}}
	found, err := s.intake().List(ctx, contract.Filter{})
	if err != nil {
		appErr, ok := classify(err).(*application.Error)
		if !ok {
			return application.Read[Downloads]{}, err
		}
		found, out.TorrentsError = []contract.Torrent{}, appErr
	}
	out.Torrents = found
	rev, err := s.revision(ctx, string(DownloadsName), in, out)
	if err != nil {
		return application.Read[Downloads]{}, err
	}
	return application.Read[Downloads]{Revision: rev, Data: out}, nil
}

type TorrentFilesInput struct {
	Hash string
}

type TorrentFiles struct {
	Torrent contract.Torrent
	Files   []contract.TorrentFile
}

func DescribeTorrentFiles() application.Definition {
	return application.Describe[TorrentFilesInput, TorrentFiles](TorrentFilesName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) TorrentFiles(ctx context.Context, in TorrentFilesInput) (application.Read[TorrentFiles], error) {
	intake := s.intake()
	found, err := intake.Get(ctx, in.Hash)
	if err != nil {
		return application.Read[TorrentFiles]{}, classify(err)
	}
	if found == nil {
		return application.Read[TorrentFiles]{}, notFound("unknown torrent")
	}
	files, err := intake.Files(ctx, in.Hash)
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
	intake := s.intake()
	found, err := intake.Get(ctx, in.Hash)
	if err != nil {
		return err
	}
	if found == nil || !found.Provenance() {
		return notFound("unknown torrent")
	}
	return classifyWrite(intake.SetWanted(ctx, s.scope(ctx, nil), in.Hash, "", []int{int(in.Index)}, in.Wanted))
}

type ControlInput struct {
	Hash        string
	Action      contract.Action
	DeleteFiles bool
	ProposalID  *int64
}

type Controlled struct {
	Hash   string
	Action contract.Action
}

func DescribeControl() application.Definition {
	return application.Describe[ControlInput, Controlled](ControlName, application.Command,
		[]application.Authority{application.Operator, application.Approved}, application.External, false)
}

func (s *Service) Control(ctx context.Context, in ControlInput) (application.Change[Controlled], error) {
	effect, err := s.control(ctx, in)
	var effects []int64
	if effect != 0 {
		effects = []int64{effect}
	}
	id, err := s.record(ctx, ControlName, in.Hash, map[string]any{"action": in.Action, "delete_files": in.DeleteFiles, "actor": s.d.Actor, "proposal_id": in.ProposalID, "effect_ids": effects}, err)
	if err != nil {
		return application.Change[Controlled]{}, err
	}
	return application.Change[Controlled]{Data: Controlled{Hash: in.Hash, Action: in.Action}, EffectIDs: effects, AuditID: id}, nil
}

func (s *Service) control(ctx context.Context, in ControlInput) (int64, error) {
	if in.Hash == "" {
		return 0, invalid("hash", "Field required")
	}
	switch in.Action {
	case contract.Pause, contract.Resume, contract.Remove:
	default:
		return 0, invalid("action", "Use pause, resume or remove.")
	}
	effect, err := s.intake().Control(ctx, contract.Control{
		Scope: s.scope(ctx, in.ProposalID), Hash: in.Hash, Action: in.Action, DeleteFiles: in.DeleteFiles,
	})
	if err != nil {
		return 0, classifyWrite(err)
	}
	return effect, nil
}
