package mediatools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
)

var (
	readKinds  = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}
	writeKinds = tool.Kinds{tool.Chat}
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
			Description: "Search the media library (movies and TV episodes) by words in the title or file name; returns paths and VPN URLs.",
			InputSchema: json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "integer", "minimum": 1, "maximum": %d}}, "required": ["query"], "additionalProperties": false}`, media.SearchLimit)),
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
	var failure media.QbitError
	if errors.As(err, &failure) {
		return tool.Error(string(failure))
	}
	return err
}

func qbitOf(settings config.MediaSettings) (*media.Qbit, error) {
	url, err := settings.NeedQbit()
	if err != nil {
		return nil, tool.NotConfigured(err.Error())
	}
	return media.NewQbit(url), nil
}

func rootOf(settings config.MediaSettings) (string, error) {
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
	case *big.Int:
		return 0, &tool.Raised{Class: "OverflowError"}
	}
	return 0, tool.Error(message)
}

func boolArg(args py.Dict, name string, def bool) (bool, error) {
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

func channelDict(c media.Channel) py.Dict {
	return py.Dict{
		{Key: "id", Value: c.ID}, {Key: "name", Value: c.Name}, {Key: "imdb", Value: c.IMDB}, {Key: "tmdb_id", Value: optionalInt(c.TMDBID)},
		{Key: "pattern", Value: c.Pattern}, {Key: "case_sensitive", Value: c.CaseSensitive}, {Key: "primed", Value: c.Primed},
		{Key: "created", Value: c.Created},
	}
}

func kindOf(isMedia bool) string {
	if isMedia {
		return "media"
	}
	return "plain"
}

func torrentDict(t media.Torrent) py.Dict {
	state := t.State
	if label, ok := media.StateLabels[t.State]; ok {
		state = label
	}
	return py.Dict{
		{Key: "hash", Value: t.Hash},
		{Key: "name", Value: t.Name},
		{Key: "state", Value: state},
		{Key: "progress_percent", Value: int64(py.Round(t.Progress*100, 0))},
		{Key: "size_bytes", Value: optionalInt(t.Size)},
		{Key: "download_bytes_per_second", Value: optionalInt(t.DLSpeed)},
		{Key: "eta_seconds", Value: optionalInt(t.ETA)},
		{Key: "kind", Value: kindOf(t.HasTag(media.TagMedia))},
		{Key: "filed", Value: t.HasTag(media.TagFiled)},
	}
}

func mediaDownloads(ctx context.Context, tc *tool.Context, _ py.Dict) (any, error) {
	settings := config.Media(tc.Env)
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
		if waiting, _, err = media.Items(ctx, qb, settings.Root); err != nil {
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
		queue = append(queue, py.Dict{{Key: "id", Value: it.ID}, {Key: "name", Value: it.Name}, {Key: "torrent", Value: it.Torrent}})
	}
	return py.Dict{
		{Key: "torrents", Value: shown},
		{Key: "waiting_to_file", Value: queue},
		{Key: "note", Value: "Only torrents of kind media are filed into the library, from the Media downloads page."},
	}, nil
}

func mediaLibrarySearch(_ context.Context, tc *tool.Context, args py.Dict) (any, error) {
	settings := config.Media(tc.Env)
	root, err := rootOf(settings)
	if err != nil {
		return nil, err
	}
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 20, media.SearchLimit, 1)
	if err != nil {
		return nil, err
	}
	links := media.NewLinks(settings, tc.Cfg.MediaLinkKeyPath())
	results := []any{}
	for _, r := range media.Search(root, query, int(limit)) {
		files, urls := make([]any, len(r.Files)), make([]any, len(r.Files))
		for i, f := range r.Files {
			files[i], urls[i] = f, links.VPNURL(f)
		}
		results = append(results, py.Dict{
			{Key: "kind", Value: r.Kind}, {Key: "title", Value: r.Title}, {Key: "year", Value: r.Year}, {Key: "dir", Value: r.Dir},
			{Key: "files", Value: files}, {Key: "vpn_urls", Value: urls},
		})
	}
	return py.Dict{
		{Key: "results", Value: results},
		{Key: "note", Value: "Paths are relative to the media root; VPN URLs work on the mesh. Public links are made on the Library page."},
	}, nil
}

func mediaChannels(ctx context.Context, tc *tool.Context, _ py.Dict) (any, error) {
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
	return py.Dict{{Key: "channels", Value: listed}, {Key: "last_check", Value: status.LastCheck}, {Key: "errors", Value: errs}}, nil
}

func mediaAddMagnet(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
	qb, err := qbitOf(config.Media(tc.Env))
	if err != nil {
		return nil, err
	}
	tags := ""
	if asMedia {
		tags = media.TagMedia
	}
	if err := qb.AddMagnet(ctx, magnet, tags); err != nil {
		return nil, qbitFailed(err)
	}
	if asMedia {
		if _, err := media.SelectFiles(ctx, qb, nil); err != nil {
			return nil, qbitFailed(err)
		}
	}
	id, err := writes.Record(ctx, tc.DB.Write, writes.New{
		TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "media_add_magnet", Target: hash,
		After: py.Dict{{Key: "magnet", Value: magnet}, {Key: "media", Value: asMedia}},
		Undo:  py.Dict{{Key: "op", Value: UndoTorrent}, {Key: "hash", Value: hash}},
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "hash", Value: hash}, {Key: "kind", Value: kindOf(asMedia)}, {Key: "write_id", Value: id}}, nil
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
	var failure media.TmdbError
	if errors.As(err, &failure) {
		return tool.Error(string(failure))
	}
	return err
}

func mediaChannelAdd(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	pattern, _, err := tool.TextArg(args, "regex", true)
	if err != nil {
		return nil, err
	}
	caseSensitive, err := boolArg(args, "case_sensitive", false)
	if err != nil {
		return nil, err
	}
	client := media.NewTMDB(config.Media(tc.Env).TMDBKey)
	tmdbID, _ := args.Get("tmdb_id")
	imdbText, _, err := tool.TextArg(args, "imdb_id", false)
	if err != nil {
		return nil, err
	}
	imdb, hasIMDB := media.IMDBDigits(imdbText)
	var name string
	var stored *int64
	switch {
	case tmdbID != nil:
		id, isInt := tmdbID.(int64)
		_, isBig := tmdbID.(*big.Int)
		if !isInt && !isBig {
			return nil, tool.Error("tmdb_id must be an integer")
		}
		if client == nil {
			return nil, tool.NotConfigured("TMDB_API_KEY is not set; give imdb_id instead")
		}
		if isBig {
			return nil, tool.Error(fmt.Sprintf("TMDB show %v has no IMDb id", tmdbID))
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
		if row, err = media.AddChannel(ctx, tx, name, imdb, pattern, caseSensitive, stored, tc.Now, false); err != nil {
			return err
		}
		writeID, err = writes.Record(ctx, tx, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "media_channel_add", Target: fmt.Sprintf("channel:%d", row.ID),
			After: channelDict(*row),
			Undo:  py.Dict{{Key: "op", Value: UndoChannelDelete}, {Key: "id", Value: row.ID}},
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
	out := append(channelDict(*row), py.Pair{Key: "write_id", Value: writeID},
		py.Pair{Key: "note", Value: "The first poll only records current releases; later matches are added."})
	return out, nil
}

func mediaChannelRemove(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
		writeID, err = writes.Record(ctx, tx, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "media_channel_remove", Target: fmt.Sprintf("channel:%d", id),
			Before: dict,
			Undo:   py.Dict{{Key: "op", Value: UndoChannelRestore}, {Key: "row", Value: dict}},
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
	return py.Dict{{Key: "removed", Value: id}, {Key: "name", Value: row.Name}, {Key: "write_id", Value: writeID}}, nil
}
