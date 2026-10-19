package mediatools

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
)

func undoTorrent(env config.Env) writes.Undoer {
	return func(ctx context.Context, _ db.Querier, info map[string]any) (any, error) {
		hash, _ := info["hash"].(string)
		url, err := config.Media(env).NeedQbit()
		if err != nil {
			return nil, writes.UndoError(err.Error())
		}
		qb := media.NewQbit(url)
		found, err := qb.Torrent(ctx, hash)
		if err == nil {
			switch {
			case found == nil:
				return nil, writes.UndoError("the torrent is no longer in qBittorrent")
			case found.HasTag(media.TagFiled):
				return nil, writes.UndoError("the torrent was already filed into the library")
			}
			err = qb.Delete(ctx, hash, true)
		}
		var failure media.QbitError
		if errors.As(err, &failure) {
			return nil, writes.UndoError(string(failure))
		}
		if err != nil {
			return nil, err
		}
		return py.Dict{{Key: "deleted", Value: hash}}, nil
	}
}

func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

func channelFailed(err error) error {
	var failure media.ChannelError
	if errors.As(err, &failure) {
		return writes.UndoError(string(failure))
	}
	return err
}

func undoChannelDelete(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	id := number(info["id"])
	if _, err := media.DeleteChannel(ctx, q, id); err != nil {
		return nil, channelFailed(err)
	}
	return py.Dict{{Key: "removed", Value: id}}, nil
}

func channelOf(row map[string]any) media.Channel {
	c := media.Channel{ID: number(row["id"])}
	c.Name, _ = row["name"].(string)
	c.IMDB, _ = row["imdb"].(string)
	c.Pattern, _ = row["pattern"].(string)
	c.CaseSensitive, _ = row["case_sensitive"].(bool)
	c.Primed, _ = row["primed"].(bool)
	c.Created, _ = row["created"].(float64)
	if row["tmdb_id"] != nil {
		id := number(row["tmdb_id"])
		c.TMDBID = &id
	}
	return c
}

func undoChannelRestore(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	row, _ := info["row"].(map[string]any)
	if _, err := media.RestoreChannel(ctx, q, channelOf(row)); err != nil {
		return nil, channelFailed(err)
	}
	return py.Dict{{Key: "restored", Value: number(row["id"])}}, nil
}
