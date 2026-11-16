package mediatools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
)

const (
	UndoChannelDelete  = mediaapp.UndoChannelDelete
	UndoChannelRestore = mediaapp.UndoChannelRestore
	searchLimitCeiling = mediaapp.TuningMediaSearchLimit
)

var (
	readKinds   = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}
	writeKinds  = tool.Kinds{tool.Chat}
	searchBound = tool.NewCeiling(20, 1, searchLimitCeiling)
)

type handlers struct {
	torrents mediaapp.Torrents
}

func Definitions(torrents mediaapp.Torrents) []module.ToolDefinition {
	tools := Tools(torrents)
	defs := make([]module.ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = module.ToolDefinition{Tool: t}
	}
	return defs
}

func Tools(torrents mediaapp.Torrents) []tool.Tool {
	h := handlers{torrents: torrents}
	return []tool.Tool{
		{
			Name:        "media_library_search",
			Description: "Search the media library (movies and TV episodes) by words in the title or file name; returns paths and VPN URLs. The limit is capped at 50.",
			InputSchema: json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": {"query": {"type": "string"}, "limit": %s}, "required": ["query"], "additionalProperties": false}`, searchBound.Schema())),
			Handler:     h.mediaLibrarySearch,
			Effect:      tool.Read,
			Annotated:   true,
			Contexts:    readKinds,
		},
		{
			Name:        "media_channels",
			Description: "List the EZTV channels (show, IMDb id, title regex, case sensitivity) with the last poll time and errors.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Handler:     h.mediaChannels,
			Effect:      tool.Read,
			Annotated:   true,
			Contexts:    readKinds,
		},
		asstapp.ApprovalGate(tool.Tool{
			Name:        "media_channel_add",
			Description: "Ask the operator to approve watching a show on EZTV; nothing changes until they approve the card. Give imdb_id (tt1234567) or tmdb_id (needs TMDB) plus a regex over release titles; case_sensitive defaults to false. New matches are downloaded automatically after the first poll. Undo removes the channel.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"imdb_id": {"type": "string"}, "tmdb_id": {"type": "integer"}, "regex": {"type": "string"}, "case_sensitive": {"type": "boolean"}, "name": {"type": "string", "description": "Display name; defaults to the TMDB title or the IMDb id."}}, "required": ["regex"], "additionalProperties": false}`),
			Effect:      tool.Propose, Undo: UndoChannelDelete,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  writeKinds,
			Approval:  &tool.Approval{Apply: h.mediaChannelAdd, Label: "Watch show", Icon: "tv", Module: unit.Media, Detail: "name"},
		}),
		asstapp.ApprovalGate(tool.Tool{
			Name:        "media_channel_remove",
			Description: "Ask the operator to approve removing an EZTV channel by id (see media_channels); nothing changes until they approve the card. Undo restores it.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": false}`),
			Effect:      tool.Propose, Undo: UndoChannelRestore,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  writeKinds,
			Approval:  &tool.Approval{Apply: h.mediaChannelRemove, Label: "Stop watching show", Icon: "tv", Module: unit.Media},
		}),
	}
}

func intArg(value any, message string) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, tool.Error(message)
		}
		return i, nil
	case *big.Int:
		return 0, tool.Error("value is too large")
	}
	return 0, tool.Error(message)
}

func boolArg(args tool.Args, name string, def bool) (bool, error) {
	value, ok := args.Get(name)
	if !ok {
		return def, nil
	}
	b, isBool := value.(bool)
	if !isBool {
		return false, tool.Error(name + " must be true or false")
	}
	return b, nil
}

func optionalInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func channelDict(c mediaapp.Channel) map[string]any {
	return map[string]any{"id": c.ID, "name": c.Name, "imdb": c.IMDB, "tmdb_id": optionalInt(c.TMDBID), "pattern": c.Pattern, "case_sensitive": c.CaseSensitive, "primed": c.Primed, "created": c.Created}
}

func (h handlers) mediaLibrarySearch(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", searchBound.Default, searchBound.Max, searchBound.Min)
	if err != nil {
		return nil, err
	}
	read, err := mediaapp.ForTool(tc, h.torrents).Library(ctx, mediaapp.LibraryInput{Query: query, Limit: int(limit)})
	if err != nil {
		return nil, tool.Fail(err)
	}
	results := []any{}
	for _, r := range read.Data.Results {
		files, urls := make([]any, len(r.Files)), make([]any, len(r.Files))
		for i, f := range r.Files {
			files[i], urls[i] = f, r.VPNURLs[i]
		}
		results = append(results, map[string]any{
			"kind": r.Kind, "title": r.Title, "year": r.Year, "dir": r.Dir,
			"files": files, "vpn_urls": urls,
		})
	}
	return map[string]any{"results": results, "catalogue": read.Data.Catalogue.State, "catalogue_observed_at": read.Data.Catalogue.ObservedAt.UTC().Format(time.RFC3339),
		"note": "Paths are relative to the media library; VPN URLs work on the mesh. Public links are made on the Library page. A stale catalogue means the media agent is unavailable and results are from the last complete scan."}, nil
}

func (h handlers) mediaChannels(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
	read, err := mediaapp.ForTool(tc, h.torrents).Channels(ctx, mediaapp.ChannelsInput{})
	if err != nil {
		return nil, tool.Fail(err)
	}
	listed := []any{}
	for _, c := range read.Data.Channels {
		listed = append(listed, channelDict(c))
	}
	errs := make([]any, len(read.Data.Status.Errors))
	for i, e := range read.Data.Status.Errors {
		errs[i] = e
	}
	return map[string]any{"channels": listed, "last_check": read.Data.Status.LastCheck, "errors": errs}, nil
}

func (h handlers) mediaChannelAdd(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	pattern, _, err := tool.TextArg(args, "regex", true)
	if err != nil {
		return nil, err
	}
	caseSensitive, err := boolArg(args, "case_sensitive", false)
	if err != nil {
		return nil, err
	}
	tmdbID, hasTMDB, err := tool.IntArg(args, "tmdb_id", false)
	if err != nil {
		return nil, err
	}
	imdb, _, err := tool.TextArg(args, "imdb_id", false)
	if err != nil {
		return nil, err
	}
	name, _, err := tool.TextArg(args, "name", false)
	if err != nil {
		return nil, err
	}
	in := mediaapp.ChannelAddInput{Name: name, IMDB: imdb, Pattern: pattern, CaseSensitive: caseSensitive, ResolveShow: true, ProposalID: tc.ProposalID}
	if hasTMDB {
		in.TMDBID = &tmdbID
	}
	added, err := mediaapp.ForTool(tc, h.torrents).ChannelAdd(ctx, in)
	if err != nil {
		return nil, tool.Fail(err)
	}
	out := channelDict(added.Data.Channel)
	if len(added.EffectIDs) > 0 {
		out["effect_id"] = added.EffectIDs[0]
	}
	out["note"] = "The first poll only records current releases; later matches are added."
	return out, nil
}

func (h handlers) mediaChannelRemove(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, _ := args.Get("id")
	id, err := intArg(raw, "id must be an integer")
	if err != nil {
		return nil, err
	}
	removed, err := mediaapp.ForTool(tc, h.torrents).ChannelRemove(ctx, mediaapp.ChannelRemoveInput{ID: id, ProposalID: tc.ProposalID})
	if err != nil {
		return nil, tool.Fail(err)
	}
	out := map[string]any{"removed": id, "name": removed.Data.Channel.Name}
	if len(removed.EffectIDs) > 0 {
		out["effect_id"] = removed.EffectIDs[0]
	}
	return out, nil
}
