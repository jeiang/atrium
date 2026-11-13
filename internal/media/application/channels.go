package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media"
)

const (
	ChannelsName       application.Name = "media.channels"
	ShowSearchName     application.Name = "media.show-search"
	ShowLookupName     application.Name = "media.show-lookup"
	ReleasePreviewName application.Name = "media.release-preview"
	ChannelAddName     application.Name = "media.channel-add"
	ChannelRemoveName  application.Name = "media.channel-remove"
	PollName           application.Name = "media.poll"

	UndoChannelDelete  = "media_channel_delete"
	UndoChannelRestore = "media_channel_restore"
)

type ChannelDeleteUndo struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
}

type ChannelRestoreUndo struct {
	Op  string      `json:"op"`
	Row ChannelUndo `json:"row"`
}

type ChannelUndo struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	IMDB          string  `json:"imdb"`
	Pattern       string  `json:"pattern"`
	CaseSensitive bool    `json:"case_sensitive"`
	Primed        bool    `json:"primed"`
	Created       float64 `json:"created"`
	TMDBID        *int64  `json:"tmdb_id"`
}

func ChannelUndoOf(c media.Channel) ChannelUndo {
	return ChannelUndo{
		ID: c.ID, Name: c.Name, IMDB: c.IMDB, Pattern: c.Pattern, CaseSensitive: c.CaseSensitive,
		Primed: c.Primed, Created: c.Created, TMDBID: c.TMDBID,
	}
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type ChannelsInput struct{}

type Channels struct {
	Channels []media.Channel
	Status   media.Status
	TMDB     bool
}

func DescribeChannels() application.Definition {
	return application.Describe[ChannelsInput, Channels](ChannelsName, application.Query,
		[]application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}

func (s *Service) Channels(ctx context.Context, in ChannelsInput) (application.Read[Channels], error) {
	var out Channels
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[Channels]{}, err
	}
	if out.Channels, err = media.ListChannels(ctx, s.d.DB.Read); err != nil {
		return application.Read[Channels]{}, err
	}
	if out.Status, err = media.ChannelStatus(ctx, s.d.DB.Read); err != nil {
		return application.Read[Channels]{}, err
	}
	if out.Status.Errors == nil {
		out.Status.Errors = []string{}
	}
	out.TMDB = set.TMDB() != nil
	counters, err := s.state.Read(ctx, nil, []string{state.Media, state.Settings})
	if err != nil {
		return application.Read[Channels]{}, err
	}
	return application.Read[Channels]{Revision: application.Revision(state.Revision(string(ChannelsName), counters, in, "operator")), Data: out}, nil
}

type ShowSearchInput struct {
	Query string
}

func DescribeShowSearch() application.Definition {
	return application.Describe[ShowSearchInput, []media.TVResult](ShowSearchName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) ShowSearch(ctx context.Context, in ShowSearchInput) (application.Read[[]media.TVResult], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[[]media.TVResult]{}, err
	}
	client := set.TMDB()
	if client == nil {
		return application.Read[[]media.TVResult]{}, &application.Error{Code: application.NotConfigured, Message: "media_tmdb_token is not set"}
	}
	found, err := client.SearchTV(ctx, strings.TrimSpace(in.Query), set.TMDBSearchLimit)
	if err != nil {
		return application.Read[[]media.TVResult]{}, classifyChannel(err, false)
	}
	rev, err := s.revision(ctx, string(ShowSearchName), in, found)
	if err != nil {
		return application.Read[[]media.TVResult]{}, err
	}
	return application.Read[[]media.TVResult]{Revision: rev, Data: found}, nil
}

type ShowLookupInput struct {
	TMDBID int64
	IMDB   string
	Name   string
}

type Show struct {
	IMDB  string
	Name  string
	Found bool
}

func DescribeShowLookup() application.Definition {
	return application.Describe[ShowLookupInput, Show](ShowLookupName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) ShowLookup(ctx context.Context, in ShowLookupInput) (application.Read[Show], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[Show]{}, err
	}
	client := set.TMDB()
	if client == nil {
		return application.Read[Show]{}, &application.Error{Code: application.NotConfigured, Message: "media_tmdb_token is not set"}
	}
	digits, found, err := client.ImdbOf(ctx, in.TMDBID)
	if err != nil {
		return application.Read[Show]{}, classifyChannel(err, false)
	}
	out := Show{Found: found}
	if found {
		out.IMDB = "tt" + digits
	}
	rev, err := s.revision(ctx, string(ShowLookupName), in, out)
	if err != nil {
		return application.Read[Show]{}, err
	}
	return application.Read[Show]{Revision: rev, Data: out}, nil
}

type ReleasePreviewInput struct {
	IMDB          string
	Pattern       string
	CaseSensitive bool
}

type ReleasePreview struct {
	Releases []media.PreviewRow
	Matched  int
}

func DescribeReleasePreview() application.Definition {
	return application.Describe[ReleasePreviewInput, ReleasePreview](ReleasePreviewName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) ReleasePreview(ctx context.Context, in ReleasePreviewInput) (application.Read[ReleasePreview], error) {
	digits, ok := media.IMDBDigits(in.IMDB)
	if !ok {
		return application.Read[ReleasePreview]{}, invalid("imdb", "Pick a show or enter an IMDb id first.")
	}
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[ReleasePreview]{}, err
	}
	releases, err := set.FetchReleases(ctx, digits)
	if err != nil {
		return application.Read[ReleasePreview]{}, classifyChannel(err, true)
	}
	var out ReleasePreview
	if strings.TrimSpace(in.Pattern) == "" {
		out.Releases = make([]media.PreviewRow, len(releases))
		for i, release := range releases {
			out.Releases[i] = media.PreviewRow{Release: release}
		}
	} else if out.Releases, err = set.Preview(releases, in.Pattern, in.CaseSensitive); err != nil {
		return application.Read[ReleasePreview]{}, classifyChannel(err, true)
	}
	for _, row := range out.Releases {
		if row.Match {
			out.Matched++
		}
	}
	rev, err := s.revision(ctx, string(ReleasePreviewName), in, out)
	if err != nil {
		return application.Read[ReleasePreview]{}, err
	}
	return application.Read[ReleasePreview]{Revision: rev, Data: out}, nil
}

type ChannelAddInput struct {
	Name          string
	IMDB          string
	TMDBID        *int64
	Pattern       string
	CaseSensitive bool
	Backfill      bool
	ResolveShow   bool
	ProposalID    *int64
}

type ChannelAdded struct {
	Channel  media.Channel
	Polled   bool
	Poll     media.PollResult
	PollFail *application.Error
}

func DescribeChannelAdd() application.Definition {
	return application.Describe[ChannelAddInput, ChannelAdded](ChannelAddName, application.Command,
		[]application.Authority{application.Operator, application.Approved}, application.Conditional, true)
}

func (s *Service) ChannelAdd(ctx context.Context, in ChannelAddInput) (application.Change[ChannelAdded], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Change[ChannelAdded]{}, err
	}
	if in.ResolveShow {
		if err := s.resolveShow(ctx, set, &in); err != nil {
			_, err = s.record(ctx, ChannelAddName, "channel:new", nil, err)
			return application.Change[ChannelAdded]{}, err
		}
	}
	var out ChannelAdded
	var effects []int64
	var id int64
	if in.ProposalID != nil {
		var effect ledger.Effect
		effect, err = s.d.Ledger.ApplyLocal(ctx, ledger.New{
			Origin: "media", Operation: "media_channel_add", OperationKey: "media_channel_add:" + ledger.NewKey(),
			Target: "channel:new", ProposalID: in.ProposalID, ThreadID: s.threadID(ctx), RunID: s.runID(ctx),
		}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
			row, err := set.AddChannel(ctx, tx, in.Name, in.IMDB, in.Pattern, in.CaseSensitive, in.TMDBID)
			if err != nil {
				return ledger.Result{}, err
			}
			out.Channel = *row
			return ledger.Result{
				State:  ledger.Done,
				Target: fmt.Sprintf("channel:%d", row.ID),
				After:  rawJSON(row),
				Undo:   rawJSON(ChannelDeleteUndo{Op: UndoChannelDelete, ID: row.ID}),
			}, nil
		})
		if err == nil {
			effects = []int64{effect.ID}
		}
		id, err = s.record(ctx, ChannelAddName, fmt.Sprintf("channel:%d", out.Channel.ID), map[string]any{"effect_ids": effects}, classifyChannel(err, true))
	} else {
		id, err = s.local(ctx, ChannelAddName, nil, func(tx *sql.Tx) (string, error) {
			row, err := set.AddChannel(ctx, tx, in.Name, in.IMDB, in.Pattern, in.CaseSensitive, in.TMDBID)
			if err != nil {
				return "channel:new", classifyChannel(err, true)
			}
			out.Channel = *row
			return fmt.Sprintf("channel:%d", row.ID), nil
		})
	}
	if err != nil {
		return application.Change[ChannelAdded]{}, err
	}
	if in.Backfill {
		out.Polled = true
		polled, err := s.Poll(ctx, PollInput{Only: &out.Channel.ID, Force: true})
		if err != nil {
			failure, err := known(err)
			if err != nil {
				return application.Change[ChannelAdded]{}, err
			}
			out.PollFail = failure
		} else {
			out.Poll = polled.Data.Result
		}
	}
	return application.Change[ChannelAdded]{Data: out, EffectIDs: effects, AuditID: id}, nil
}

func (s *Service) resolveShow(ctx context.Context, set media.Settings, in *ChannelAddInput) error {
	client := set.TMDB()
	digits, hasIMDB := media.IMDBDigits(in.IMDB)
	switch {
	case in.TMDBID != nil:
		if client == nil {
			return &application.Error{Code: application.NotConfigured, Message: "media_tmdb_token is not set; give imdb_id instead"}
		}
		imdb, found, err := client.ImdbOf(ctx, *in.TMDBID)
		if err != nil {
			return classifyChannel(err, false)
		}
		show, err := client.Show(ctx, *in.TMDBID)
		if err != nil {
			return classifyChannel(err, false)
		}
		if !found || len(show) == 0 {
			return invalid("tmdb_id", fmt.Sprintf("TMDB show %d has no IMDb id", *in.TMDBID))
		}
		in.IMDB, in.Name = imdb, "tt"+imdb
		if text, isText := show["name"].(string); isText && text != "" {
			in.Name = text
		}
	case hasIMDB:
		in.IMDB = digits
		if in.Name != "" {
			return nil
		}
		in.Name = "tt" + digits
		if client == nil {
			return nil
		}
		found, err := client.ShowByImdb(ctx, digits)
		if err != nil {
			return classifyChannel(err, false)
		}
		if found != nil && found.Name != "" {
			in.Name = found.Name
		}
	default:
		return invalid("imdb_id", "give imdb_id (tt1234567) or tmdb_id")
	}
	return nil
}

type ChannelRemoveInput struct {
	ID         int64
	ProposalID *int64
}

type ChannelRemoved struct {
	Channel media.Channel
}

func DescribeChannelRemove() application.Definition {
	return application.Describe[ChannelRemoveInput, ChannelRemoved](ChannelRemoveName, application.Command,
		[]application.Authority{application.Operator, application.Approved}, application.Conditional, true)
}

func (s *Service) ChannelRemove(ctx context.Context, in ChannelRemoveInput) (application.Change[ChannelRemoved], error) {
	target := fmt.Sprintf("channel:%d", in.ID)
	var out ChannelRemoved
	var effects []int64
	var id int64
	var err error
	if in.ProposalID != nil {
		var effect ledger.Effect
		effect, err = s.d.Ledger.ApplyLocal(ctx, ledger.New{
			Origin: "media", Operation: "media_channel_remove", OperationKey: "media_channel_remove:" + ledger.NewKey(),
			Target: target, ProposalID: in.ProposalID, ThreadID: s.threadID(ctx), RunID: s.runID(ctx),
		}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
			row, err := media.DeleteChannel(ctx, tx, in.ID)
			if err != nil {
				return ledger.Result{}, err
			}
			out.Channel = row
			return ledger.Result{
				State:  ledger.Done,
				Target: target,
				Before: rawJSON(row),
				Undo:   rawJSON(ChannelRestoreUndo{Op: UndoChannelRestore, Row: ChannelUndoOf(row)}),
			}, nil
		})
		if err == nil {
			effects = []int64{effect.ID}
		}
		id, err = s.record(ctx, ChannelRemoveName, target, map[string]any{"effect_ids": effects}, classifyChannel(err, false))
	} else {
		id, err = s.local(ctx, ChannelRemoveName, nil, func(tx *sql.Tx) (string, error) {
			row, err := media.DeleteChannel(ctx, tx, in.ID)
			if err != nil {
				return target, classifyChannel(err, false)
			}
			out.Channel = row
			return target, nil
		})
	}
	if err != nil {
		return application.Change[ChannelRemoved]{}, err
	}
	return application.Change[ChannelRemoved]{Data: out, EffectIDs: effects, AuditID: id}, nil
}

type PollInput struct {
	Only                *int64
	Force               bool
	SkipWithoutChannels bool
}

type Polled struct {
	Result media.PollResult
	Ran    bool
}

func DescribePoll() application.Definition {
	return application.Describe[PollInput, Polled](PollName, application.Command,
		[]application.Authority{application.Operator, application.System}, application.External, true)
}

func (s *Service) Poll(ctx context.Context, in PollInput) (application.Change[Polled], error) {
	out, err := s.poll(ctx, in)
	id, err := s.record(ctx, PollName, "", map[string]any{"added": len(out.Result.Added), "errors": len(out.Result.Errors)}, err)
	if err != nil {
		return application.Change[Polled]{}, err
	}
	return application.Change[Polled]{Data: out, AuditID: id}, nil
}

func (s *Service) poll(ctx context.Context, in PollInput) (Polled, error) {
	if in.SkipWithoutChannels {
		found, err := media.ListChannels(ctx, s.d.DB.Write)
		if err != nil || len(found) == 0 {
			return Polled{}, err
		}
	}
	set, err := s.settings(ctx)
	if err != nil {
		return Polled{}, err
	}
	result, err := media.Poll(ctx, s.d.DB, s.d.Ledger, s.d.Env, set, media.PollOptions{Now: s.d.Now, Only: in.Only, Force: in.Force})
	if err != nil {
		return Polled{}, classify(err)
	}
	return Polled{Result: result, Ran: true}, nil
}

func classifyChannel(err error, invalidInput bool) error {
	var failure media.ChannelError
	var eztv media.EztvError
	var tmdb media.TmdbError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &failure) && invalidInput:
		return &application.Error{Code: application.Invalid, Message: failure.Error(), Cause: err}
	case errors.As(err, &failure):
		return &application.Error{Code: application.NotFound, Message: failure.Error(), Cause: err}
	case errors.As(err, &eztv), errors.As(err, &tmdb):
		return &application.Error{Code: application.Unavailable, Message: err.Error(), Cause: err}
	}
	return classify(err)
}
