package mediatools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
)

type torrentUndo struct {
	Op   string `json:"op"`
	Hash string `json:"hash"`
}

type channelDeleteUndo struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
}

type channelRestoreUndo struct {
	Op  string      `json:"op"`
	Row channelUndo `json:"row"`
}

type channelUndo struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	IMDB          string  `json:"imdb"`
	Pattern       string  `json:"pattern"`
	CaseSensitive bool    `json:"case_sensitive"`
	Primed        bool    `json:"primed"`
	Created       float64 `json:"created"`
	TMDBID        *int64  `json:"tmdb_id"`
}

func channelUndoOf(c media.Channel) channelUndo {
	return channelUndo{
		ID: c.ID, Name: c.Name, IMDB: c.IMDB, Pattern: c.Pattern, CaseSensitive: c.CaseSensitive,
		Primed: c.Primed, Created: c.Created, TMDBID: c.TMDBID,
	}
}

func (c channelUndo) channel() media.Channel {
	return media.Channel{
		ID: c.ID, Name: c.Name, IMDB: c.IMDB, Pattern: c.Pattern, CaseSensitive: c.CaseSensitive,
		Primed: c.Primed, Created: c.Created, TMDBID: c.TMDBID,
	}
}

func undoTorrent(env config.Env) writes.Undoer {
	return func(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
		var u torrentUndo
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, writes.UndoError("media undo payload is invalid")
		}
		hash := u.Hash
		settings, err := media.Load(ctx, q, env)
		if err != nil {
			return nil, err
		}
		qb, err := settings.Qbit()
		if err != nil {
			return nil, writes.UndoError(err.Error())
		}
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

func channelFailed(err error) error {
	return tool.Domain[media.ChannelError, writes.UndoError](err)
}

func undoChannelDelete(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u channelDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, writes.UndoError("media undo payload is invalid")
	}
	id := u.ID
	if _, err := media.DeleteChannel(ctx, q, id); err != nil {
		return nil, channelFailed(err)
	}
	return py.Dict{{Key: "removed", Value: id}}, nil
}

func undoChannelRestore(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u channelRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, writes.UndoError("media undo payload is invalid")
	}
	if _, err := media.RestoreChannel(ctx, q, u.Row.channel()); err != nil {
		return nil, channelFailed(err)
	}
	return py.Dict{{Key: "restored", Value: u.Row.ID}}, nil
}
