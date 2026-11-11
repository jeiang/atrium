package mediatools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/tool"
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

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func validateOp(name string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return ledger.Refused("media undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("media undo payload names " + head.Op)
		}
		return nil
	}
}

func undoTorrent(store *ledger.Store, env config.Env) ledger.Undoer {
	return ledger.Undoer{
		Validate: validateOp(UndoTorrent),
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u torrentUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.Hash == "" {
				return ledger.Result{}, ledger.Refused("media undo payload is invalid")
			}
			hash := u.Hash
			settings, err := media.Load(ctx, store.DB.Read, env)
			if err != nil {
				return ledger.Result{}, err
			}
			qb, err := settings.Qbit()
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			found, err := qb.Torrent(ctx, hash)
			if err == nil {
				switch {
				case found == nil:
					return ledger.Result{State: ledger.Failed, Error: "the torrent is no longer in qBittorrent"}, nil
				case found.HasTag(media.TagFiled):
					return ledger.Result{State: ledger.Failed, Error: "the torrent was already filed into the library"}, nil
				}
				err = qb.Delete(ctx, hash, true)
			}
			var failure media.QbitError
			if errors.As(err, &failure) {
				return ledger.Result{State: ledger.Failed, Error: string(failure)}, nil
			}
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"deleted": hash})}, nil
		},
	}
}

func channelFailed(err error) error {
	return tool.Domain[media.ChannelError, ledger.Refused](err)
}

func undoChannelDelete(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (any, error) {
	var u channelDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("media undo payload is invalid")
	}
	id := u.ID
	if _, err := media.DeleteChannel(ctx, tx, id); err != nil {
		return nil, channelFailed(err)
	}
	return map[string]any{"removed": id}, nil
}

func undoChannelRestore(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (any, error) {
	var u channelRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("media undo payload is invalid")
	}
	if _, err := media.RestoreChannel(ctx, tx, u.Row.channel()); err != nil {
		return nil, channelFailed(err)
	}
	return map[string]any{"restored": u.Row.ID}, nil
}

func RegisterLedgerUndoer(s *ledger.Store, env config.Env) error {
	if err := s.RegisterUndo(UndoTorrent, undoTorrent(s, env)); err != nil {
		return err
	}
	if err := s.RegisterUndo(UndoChannelDelete, ledger.Undoer{
		Validate: validateOp(UndoChannelDelete),
		Local: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
			value, err := undoChannelDelete(ctx, tx, raw)
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
		},
	}); err != nil {
		return err
	}
	return s.RegisterUndo(UndoChannelRestore, ledger.Undoer{
		Validate: validateOp(UndoChannelRestore),
		Local: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
			value, err := undoChannelRestore(ctx, tx, raw)
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
		},
	})
}
