package mediatools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	UndoTorrent        = "media_torrent_delete"
	UndoChannelDelete  = "media_channel_delete"
	UndoChannelRestore = "media_channel_restore"
	searchLimitCeiling = media.TuningMediaSearchLimit
)

var (
	readKinds   = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}
	writeKinds  = tool.Kinds{tool.Chat}
	searchBound = tool.NewCeiling(20, 1, searchLimitCeiling)
)

func Definitions() []module.ToolDefinition {
	tools := Tools()
	defs := make([]module.ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = module.ToolDefinition{Tool: t}
	}
	return defs
}

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "media_downloads",
			Description: "List torrents in qBittorrent (name, state, progress, size, speed, ETA, media or plain) and the completed media torrents and import entries waiting to be filed.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Handler:     mediaDownloads,
			Effect:      tool.Read,
			Annotated:   true,
			Contexts:    readKinds,
		},
		{
			Name:        "media_library_search",
			Description: "Search the media library (movies and TV episodes) by words in the title or file name; returns paths and VPN URLs. The limit is capped at 50.",
			InputSchema: json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": {"query": {"type": "string"}, "limit": %s}, "required": ["query"], "additionalProperties": false}`, searchBound.Schema())),
			Handler:     mediaLibrarySearch,
			Effect:      tool.Read,
			Annotated:   true,
			Contexts:    readKinds,
		},
		{
			Name:        "media_channels",
			Description: "List the EZTV channels (show, IMDb id, title regex, case sensitivity) with the last poll time and errors.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Handler:     mediaChannels,
			Effect:      tool.Read,
			Annotated:   true,
			Contexts:    readKinds,
		},
		approval.Gate(tool.Tool{
			Name:        "media_add_magnet",
			Description: "Ask the operator to approve adding a magnet link to qBittorrent; nothing is added until they approve the card. media=true (default) tags it for filing into the library with automatic video/subtitle selection; false adds a plain torrent. Undo deletes the torrent and its data unless it was already filed.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"magnet": {"type": "string"}, "media": {"type": "boolean"}}, "required": ["magnet"], "additionalProperties": false}`),
			Effect:      tool.Propose, Undo: UndoTorrent,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  writeKinds,
			Approval:  &tool.Approval{Apply: mediaAddMagnet, Check: checkAddMagnet, Label: "Add torrent", Icon: "download", Module: module.Media},
		}),
		approval.Gate(tool.Tool{
			Name:        "media_channel_add",
			Description: "Ask the operator to approve watching a show on EZTV; nothing changes until they approve the card. Give imdb_id (tt1234567) or tmdb_id (needs TMDB) plus a regex over release titles; case_sensitive defaults to false. New matches are downloaded automatically after the first poll. Undo removes the channel.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"imdb_id": {"type": "string"}, "tmdb_id": {"type": "integer"}, "regex": {"type": "string"}, "case_sensitive": {"type": "boolean"}, "name": {"type": "string", "description": "Display name; defaults to the TMDB title or the IMDb id."}}, "required": ["regex"], "additionalProperties": false}`),
			Effect:      tool.Propose, Undo: UndoChannelDelete,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  writeKinds,
			Approval:  &tool.Approval{Apply: mediaChannelAdd, Label: "Watch show", Icon: "tv", Module: module.Media, Detail: "name"},
		}),
		approval.Gate(tool.Tool{
			Name:        "media_channel_remove",
			Description: "Ask the operator to approve removing an EZTV channel by id (see media_channels); nothing changes until they approve the card. Undo restores it.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": false}`),
			Effect:      tool.Propose, Undo: UndoChannelRestore,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  writeKinds,
			Approval:  &tool.Approval{Apply: mediaChannelRemove, Label: "Stop watching show", Icon: "tv", Module: module.Media},
		}),
	}
}

func qbitFailed(err error) error {
	var conflict *ledger.Conflict
	if errors.As(err, &conflict) {
		return tool.Error("the torrent is blocked by an unresolved earlier add")
	}
	var status *media.QbitStatusError
	if errors.As(err, &status) {
		return tool.Error(status.Error())
	}
	return tool.Domain[media.QbitError, tool.Error](err)
}

func qbitOf(settings media.Settings) (*media.Qbit, error) {
	qb, err := settings.Qbit()
	if err != nil {
		return nil, tool.NotConfigured(err.Error())
	}
	return qb, nil
}

func rootOf(settings media.Settings) (string, error) {
	root, err := settings.NeedRoot()
	if err != nil {
		return "", tool.NotConfigured(err.Error())
	}
	return root, nil
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

func channelDict(c media.Channel) map[string]any {
	return map[string]any{"id": c.ID, "name": c.Name, "imdb": c.IMDB, "tmdb_id": optionalInt(c.TMDBID), "pattern": c.Pattern, "case_sensitive": c.CaseSensitive, "primed": c.Primed, "created": c.Created}
}

func kindOf(isMedia bool) string {
	if isMedia {
		return "media"
	}
	return "plain"
}

func torrentDict(t media.Torrent) map[string]any {
	state := t.State
	if label, ok := media.StateLabels[t.State]; ok {
		state = label
	}
	return map[string]any{"hash": t.Hash, "name": t.Name, "state": state, "progress_percent": int64(math.RoundToEven(t.Progress * 100)), "size_bytes": optionalInt(t.Size), "download_bytes_per_second": optionalInt(t.DLSpeed), "eta_seconds": optionalInt(t.ETA), "kind": kindOf(t.HasTag(media.TagMedia)), "filed": t.HasTag(media.TagFiled)}
}

func mediaDownloads(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
	read, err := mediaapp.ForTool(tc).Downloads(ctx, mediaapp.DownloadsInput{})
	if err != nil {
		return nil, tool.Fail(err)
	}
	if read.Data.TorrentsError != nil {
		return nil, tool.Fail(read.Data.TorrentsError)
	}
	torrents := read.Data.Torrents
	torrentLimit := assistant.TuningAgentMediaTorrentLimit
	shown := []any{}
	for _, t := range torrents[:min(torrentLimit, len(torrents))] {
		shown = append(shown, torrentDict(t))
	}
	queue := []any{}
	for _, it := range read.Data.Queue {
		queue = append(queue, map[string]any{"id": it.Item.ID, "name": it.Item.Name, "torrent": it.Item.Torrent})
	}
	return map[string]any{"torrents": shown, "waiting_to_file": queue, "note": "Only torrents of kind media are filed into the library, from the Media downloads page."}, nil
}

func mediaLibrarySearch(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	settings, err := media.Load(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	root, err := rootOf(settings)
	if err != nil {
		return nil, err
	}
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", searchBound.Default, searchBound.Max, searchBound.Min)
	if err != nil {
		return nil, err
	}
	results := []any{}
	for _, r := range settings.Search(root, query, int(limit)) {
		files, urls := make([]any, len(r.Files)), make([]any, len(r.Files))
		for i, f := range r.Files {
			files[i], urls[i] = f, settings.VPNURL(f)
		}
		results = append(results, map[string]any{
			"kind": r.Kind, "title": r.Title, "year": r.Year, "dir": r.Dir,
			"files": files, "vpn_urls": urls,
		})
	}
	return map[string]any{"results": results, "note": "Paths are relative to the media root; VPN URLs work on the mesh. Public links are made on the Library page."}, nil
}

func mediaChannels(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
	rows, err := media.ListChannels(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	status, err := media.ChannelStatus(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	listed := []any{}
	for _, c := range rows {
		listed = append(listed, channelDict(c))
	}
	errs := make([]any, len(status.Errors))
	for i, e := range status.Errors {
		errs[i] = e
	}
	return map[string]any{"channels": listed, "last_check": status.LastCheck, "errors": errs}, nil
}

func addMagnetArgs(args tool.Args) (string, bool, error) {
	magnet, _, err := tool.TextArg(args, "magnet", true)
	if err != nil {
		return "", false, err
	}
	asMedia, err := boolArg(args, "media", true)
	if err != nil {
		return "", false, err
	}
	return magnet, asMedia, nil
}

func checkAddMagnet(_ context.Context, _ *tool.Context, args tool.Args) error {
	magnet, _, err := addMagnetArgs(args)
	if err != nil {
		return err
	}
	if _, err := mediaapp.CheckMagnet(magnet); err != nil {
		return tool.Fail(err)
	}
	return nil
}

func mediaAddMagnet(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	magnet, asMedia, err := addMagnetArgs(args)
	if err != nil {
		return nil, err
	}
	added, err := mediaapp.ForTool(tc).Add(ctx, mediaapp.AddInput{Magnet: &mediaapp.Magnet{URI: magnet}, Media: asMedia, ProposalID: tc.ProposalID})
	if err != nil {
		return nil, tool.Fail(err)
	}
	return map[string]any{"hash": added.Data.Hash, "kind": added.Data.Kind}, nil
}

func showFromTMDB(ctx context.Context, client *media.TMDB, tmdbID int64) (string, string, error) {
	imdb, found, err := client.ImdbOf(ctx, tmdbID)
	if err != nil {
		return "", "", tmdbFailed(err)
	}
	show, err := client.Show(ctx, tmdbID)
	if err != nil {
		return "", "", tmdbFailed(err)
	}
	if !found || len(show) == 0 {
		return "", "", tool.Error(fmt.Sprintf("TMDB show %d has no IMDb id", tmdbID))
	}
	if text, isText := show["name"].(string); isText && text != "" {
		return imdb, text, nil
	}
	return imdb, "tt" + imdb, nil
}

func showFromIMDB(ctx context.Context, client *media.TMDB, imdb, name string) (string, string, error) {
	if name != "" {
		return imdb, name, nil
	}
	if client == nil {
		return imdb, "tt" + imdb, nil
	}
	found, err := client.ShowByImdb(ctx, imdb)
	if err != nil {
		return "", "", tmdbFailed(err)
	}
	if found != nil && found.Name != "" {
		return imdb, found.Name, nil
	}
	return imdb, "tt" + imdb, nil
}

func tmdbFailed(err error) error {
	return tool.Domain[media.TmdbError, tool.Error](err)
}

func mediaChannelAdd(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	pattern, _, err := tool.TextArg(args, "regex", true)
	if err != nil {
		return nil, err
	}
	caseSensitive, err := boolArg(args, "case_sensitive", false)
	if err != nil {
		return nil, err
	}
	settings, err := media.Load(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	client := settings.TMDB()
	tmdbID, hasTMDB, err := tool.IntArg(args, "tmdb_id", false)
	if err != nil {
		return nil, err
	}
	imdbText, _, err := tool.TextArg(args, "imdb_id", false)
	if err != nil {
		return nil, err
	}
	imdb, hasIMDB := media.IMDBDigits(imdbText)
	var name string
	var stored *int64
	switch {
	case hasTMDB:
		id := tmdbID
		if client == nil {
			return nil, tool.NotConfigured("media_tmdb_token is not set; give imdb_id instead")
		}
		var err error
		if imdb, name, err = showFromTMDB(ctx, client, id); err != nil {
			return nil, err
		}
		stored = &id
	case hasIMDB:
		display, _, err := tool.TextArg(args, "name", false)
		if err != nil {
			return nil, err
		}
		if imdb, name, err = showFromIMDB(ctx, client, imdb, display); err != nil {
			return nil, err
		}
	default:
		return nil, tool.Error("give imdb_id (tt1234567) or tmdb_id")
	}
	var row *media.Channel
	effect, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "media", Operation: "media_channel_add", OperationKey: "media_channel_add:" + ledger.NewKey(),
		Target: "channel:new", ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		var err error
		if row, err = settings.AddChannel(ctx, tx, name, imdb, pattern, caseSensitive, stored); err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: fmt.Sprintf("channel:%d", row.ID),
			After:  jsonOf(channelDict(*row)),
			Undo:   jsonOf(channelDeleteUndo{Op: UndoChannelDelete, ID: row.ID}),
		}, nil
	})
	if err != nil {
		var failure media.ChannelError
		if errors.As(err, &failure) {
			return nil, tool.Error(string(failure))
		}
		return nil, err
	}
	out := channelDict(*row)
	out["effect_id"] = effect.ID
	out["note"] = "The first poll only records current releases; later matches are added."
	return out, nil
}

func mediaChannelRemove(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, _ := args.Get("id")
	id, err := intArg(raw, "id must be an integer")
	if err != nil {
		return nil, err
	}
	var row media.Channel
	effect, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "media", Operation: "media_channel_remove", OperationKey: "media_channel_remove:" + ledger.NewKey(),
		Target: fmt.Sprintf("channel:%d", id), ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		var err error
		if row, err = media.DeleteChannel(ctx, tx, id); err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: fmt.Sprintf("channel:%d", id),
			Before: jsonOf(channelDict(row)),
			Undo:   jsonOf(channelRestoreUndo{Op: UndoChannelRestore, Row: channelUndoOf(row)}),
		}, nil
	})
	if err != nil {
		var failure media.ChannelError
		if errors.As(err, &failure) {
			return nil, tool.Error(string(failure))
		}
		return nil, err
	}
	return map[string]any{"removed": id, "name": row.Name, "effect_id": effect.ID}, nil
}
