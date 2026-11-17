package application

import (
	"context"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/library/mediaagent/protocol"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/notify"
)

const RequestsName application.Name = "media.requests"

const LibraryPage = "/apps/media/library"

type RequestsInput struct{}

type Requests struct {
	Requests []library.Request
}

func DescribeRequests() application.Definition {
	return application.Describe[RequestsInput, Requests](RequestsName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) Requests(ctx context.Context, in RequestsInput) (application.Read[Requests], error) {
	rows, err := library.ListRequests(ctx, s.d.DB.Read)
	if err != nil {
		return application.Read[Requests]{}, err
	}
	out := Requests{Requests: rows}
	rev, err := s.revision(ctx, string(RequestsName), in, out)
	if err != nil {
		return application.Read[Requests]{}, err
	}
	return application.Read[Requests]{Revision: rev, Data: out}, nil
}

func (s *Service) autoFile(ctx context.Context, set library.Settings) []string {
	filed := []string{}
	agent, err := set.Agent(protocol.CapabilityFile)
	if err != nil {
		return filed
	}
	torrents, err := set.Intake.List(ctx, contract.Filter{Completed: true, Label: library.TagMedia})
	if err != nil {
		return filed
	}
	var shows []string
	listed := false
	for _, t := range torrents {
		if !library.Waiting(t) {
			continue
		}
		req, ok := s.approved(ctx, t.Hash)
		if !ok {
			continue
		}
		if !listed {
			shows, listed = library.Shows(ctx, agent), true
		}
		at := located{set: set, agent: agent, item: set.TorrentItem(ctx, agent, set.DownloadPrefix, t), shows: shows}
		plan, reason := at.exact(ctx)
		if reason != "" {
			s.noteRequest(ctx, at, 0, contract.Job, errors.New(reason))
			continue
		}
		out, err := s.file(ctx, at, plan.Ops, contract.Job)
		_, err = s.record(ctx, FileItemName, at.item.ID, map[string]any{"placed": len(out.Dests), "automatic": true}, err)
		if err != nil {
			continue
		}
		filed = append(filed, at.item.Name)
		s.tell(ctx, set, plan, out, req)
	}
	return filed
}

func (s *Service) approved(ctx context.Context, hash string) (*library.Request, bool) {
	q := s.d.DB.Read
	req, err := library.RequestFor(ctx, q, hash)
	if err != nil {
		logx.Logger("atrium.media").Warn("auto-file: reading the request failed", "err", err)
		return nil, false
	}
	if req == nil {
		followed, err := library.Followed(ctx, s.d.Ledger, q, hash)
		if err != nil || !followed {
			return nil, false
		}
	}
	attempted, err := library.FilingAttempted(ctx, s.d.Ledger, hash)
	if err != nil || attempted {
		return nil, false
	}
	return req, true
}

func (s *Service) noteRequest(ctx context.Context, at located, effectID int64, actor contract.Actor, err error) {
	if !at.item.Torrent {
		return
	}
	u := library.RequestUpdate{Download: at.item.Name, EffectID: effectID}
	switch {
	case err == nil:
		u.State, u.FiledBy = library.RequestFiled, string(actor)
	case effectID != 0:
		u.State, u.FiledBy, u.Reason = library.RequestFailed, string(actor), err.Error()
	case actor == contract.Job:
		u.State, u.Reason = library.RequestWaiting, err.Error()
	default:
		return
	}
	if err := library.UpdateRequest(ctx, s.d.DB.Write, at.item.ID, u, s.d.Now()); err != nil {
		logx.Logger("atrium.media").Warn("auto-file: updating the request failed", "err", err)
	}
}

func (s *Service) tell(ctx context.Context, set library.Settings, plan FilingPreview, out Filed, req *library.Request) {
	if !notify.Enabled(ctx) {
		return
	}
	title := plan.View.Title + " (" + plan.View.Year + ")"
	m := notify.Message{Title: "In the library: " + title, Body: out.Item.Name, Channel: library.NotifyChannel, Now: s.d.Now,
		Occurrence: "media-filed:" + strconv.FormatInt(out.EffectID, 10)}
	if req != nil && req.ThreadID != nil {
		m.ThreadID = *req.ThreadID
	}
	if set.PublicURL != "" {
		m.Click = set.PublicURL + LibraryPage
	}
	if _, err := notify.Send(ctx, s.d.DB, m); err != nil {
		logx.Logger("atrium.media").Warn("auto-file: notification failed", "err", err)
	}
}
