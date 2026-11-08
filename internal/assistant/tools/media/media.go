package mediatools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
)

const (
	UndoTorrent        = "media_torrent_delete"
	UndoChannelDelete  = "media_channel_delete"
	UndoChannelRestore = "media_channel_restore"
	searchLimitCeiling = 500
)

var (
	readKinds   = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}
	writeKinds  = tool.Kinds{tool.Chat}
	searchBound = tool.NewCeiling(20, 1, searchLimitCeiling)
)

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "media_downloads",
			Description: "List torrents in qBittorrent (name, state, progress, size, speed, ETA, media or plain) and the completed media torrents and import entries waiting to be filed.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Handler:     mediaDownloads,
			Effect:      tool.Read,
			Contexts:    readKinds,
		},
		{
			Name:        "media_library_search",
			Description: "Search the media library (movies and TV episodes) by words in the title or file name; returns paths and VPN URLs. The limit is capped by media_search_limit.",
			InputSchema: json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": {"query": {"type": "string"}, "limit": %s}, "required": ["query"], "additionalProperties": false}`, searchBound.Schema())),
			Handler:     mediaLibrarySearch,
			Effect:      tool.Read,
			Contexts:    readKinds,
		},
		{
			Name:        "media_channels",
			Description: "List the EZTV channels (show, IMDb id, title regex, case sensitivity) with the last poll time and errors.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Handler:     mediaChannels,
			Effect:      tool.Read,
			Contexts:    readKinds,
		},
		{
			Name:        "media_add_magnet",
			Description: "Add a magnet link to qBittorrent. media=true (default) tags it for filing into the library with automatic video/subtitle selection; false adds a plain torrent. Undo deletes the torrent and its data unless it was already filed.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"magnet": {"type": "string"}, "media": {"type": "boolean"}}, "required": ["magnet"], "additionalProperties": false}`),
			Handler:     mediaAddMagnet,
			Effect:      tool.Write,
			Contexts:    writeKinds,
		},
		{
			Name:        "media_channel_add",
			Description: "Watch a show on EZTV: give imdb_id (tt1234567) or tmdb_id (needs TMDB) plus a regex over release titles; case_sensitive defaults to false. New matches are downloaded automatically after the first poll. Undo removes the channel.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"imdb_id": {"type": "string"}, "tmdb_id": {"type": "integer"}, "regex": {"type": "string"}, "case_sensitive": {"type": "boolean"}, "name": {"type": "string", "description": "Display name; defaults to the TMDB title or the IMDb id."}}, "required": ["regex"], "additionalProperties": false}`),
			Handler:     mediaChannelAdd,
			Effect:      tool.Write,
			Contexts:    writeKinds,
		},
		{
			Name:        "media_channel_remove",
			Description: "Remove an EZTV channel by id (see media_channels). Undo restores it.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": false}`),
			Handler:     mediaChannelRemove,
			Effect:      tool.Write,
			Contexts:    writeKinds,
		},
	}
}

func RegisterUndoer(r *writes.Registry, env config.Env) {
	r.Register(UndoTorrent, undoTorrent(env))
	r.Register(UndoChannelDelete, undoChannelDelete)
	r.Register(UndoChannelRestore, undoChannelRestore)
}

func qbitFailed(err error) error {
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
	settings, err := media.Load(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	qb, err := qbitOf(settings)
	if err != nil {
		return nil, err
	}
	torrents, err := qb.Torrents(ctx, "", "", "")
	if err != nil {
		return nil, qbitFailed(err)
	}
	waiting := []media.Item{}
	if settings.Root != "" {
		if waiting, _, err = settings.Items(ctx, qb, settings.Root); err != nil {
			return nil, err
		}
	}
	r := assistant.Read(ctx, tc.DB.Read)
	torrentLimit := r.Int(assistant.KeyMediaTorrentLimit)
	if err := r.Err(); err != nil {
		return nil, err
	}
	shown := []any{}
	for _, t := range torrents[:min(torrentLimit, len(torrents))] {
		shown = append(shown, torrentDict(t))
	}
	queue := []any{}
	for _, it := range waiting {
		queue = append(queue, map[string]any{"id": it.ID, "name": it.Name, "torrent": it.Torrent})
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
	limit, err := tool.BoundedInt(args, "limit", searchBound.Default, min(int64(settings.SearchLimit), searchBound.Max), searchBound.Min)
	if err != nil {
		return nil, err
	}
	results := []any{}
	for _, r := range settings.Search(root, query, int(limit)) {
		files, urls := make([]any, len(r.Files)), make([]any, len(r.Files))
		for i, f := range r.Files {
			files[i], urls[i] = f, settings.VPNURL(f)
		}
		results = append(results, py.Dict{
			{Key: "kind", Value: r.Kind}, {Key: "title", Value: r.Title}, {Key: "year", Value: r.Year}, {Key: "dir", Value: r.Dir},
			{Key: "files", Value: files}, {Key: "vpn_urls", Value: urls},
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

func mediaAddMagnet(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	magnet, _, err := tool.TextArg(args, "magnet", true)
	if err != nil {
		return nil, err
	}
	asMedia, err := boolArg(args, "media", true)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(magnet, "magnet:?") {
		return nil, tool.Error("magnet must be a magnet: link")
	}
	hash, ok := media.MagnetHash(magnet)
	if !ok {
		return nil, tool.Error("the magnet link has no v1 info hash (xt=urn:btih:...)")
	}
	settings, err := media.Load(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	qb, err := qbitOf(settings)
	if err != nil {
		return nil, err
	}
	tags := ""
	if asMedia {
		tags = media.TagMedia
	}
	id, err := writes.Begin(ctx, tc.DB.Write, writes.New{
		TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "media_add_magnet", Target: hash,
	})
	if err != nil {
		return nil, err
	}
	if err := qb.AddMagnet(ctx, magnet, tags); err != nil {
		_ = writes.Discard(ctx, tc.DB.Write, id)
		return nil, qbitFailed(err)
	}
	if asMedia {
		if _, err := settings.SelectFiles(ctx, qb, nil); err != nil {
			return nil, qbitFailed(err)
		}
	}
	if err := writes.Settle(ctx, tc.DB.Write, tc.Undoers, id, writes.New{
		Target: hash,
		After:  map[string]any{"magnet": magnet, "media": asMedia},
		Undo:   torrentUndo{Op: UndoTorrent, Hash: hash},
	}); err != nil {
		return nil, err
	}
	return map[string]any{"hash": hash, "kind": kindOf(asMedia), "write_id": id}, nil
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
	if name, ok := show.Get("name"); ok {
		if text, isText := name.(string); isText && text != "" {
			return imdb, text, nil
		}
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
	var writeID int64
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if row, err = settings.AddChannel(ctx, tx, name, imdb, pattern, caseSensitive, stored); err != nil {
			return err
		}
		writeID, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "media_channel_add", Target: fmt.Sprintf("channel:%d", row.ID),
			After: channelDict(*row),
			Undo:  channelDeleteUndo{Op: UndoChannelDelete, ID: row.ID},
		})
		return err
	})
	if err != nil {
		var failure media.ChannelError
		if errors.As(err, &failure) {
			return nil, tool.Error(string(failure))
		}
		return nil, err
	}
	out := channelDict(*row)
	out["write_id"] = writeID
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
	var writeID int64
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if row, err = media.DeleteChannel(ctx, tx, id); err != nil {
			return err
		}
		dict := channelDict(row)
		writeID, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "media_channel_remove", Target: fmt.Sprintf("channel:%d", id),
			Before: dict,
			Undo:   channelRestoreUndo{Op: UndoChannelRestore, Row: channelUndoOf(row)},
		})
		return err
	})
	if err != nil {
		var failure media.ChannelError
		if errors.As(err, &failure) {
			return nil, tool.Error(string(failure))
		}
		return nil, err
	}
	return map[string]any{"removed": id, "name": row.Name, "write_id": writeID}, nil
}
