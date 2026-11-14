package application

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/tool"
)

func channelOf(c ChannelUndo) media.Channel {
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

func channelFailed(err error) error {
	return tool.Domain[media.ChannelError, ledger.Refused](err)
}

func undoChannelDelete(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (any, error) {
	var u ChannelDeleteUndo
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
	var u ChannelRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("media undo payload is invalid")
	}
	if _, err := media.RestoreChannel(ctx, tx, channelOf(u.Row)); err != nil {
		return nil, channelFailed(err)
	}
	return map[string]any{"restored": u.Row.ID}, nil
}

func Undo(s *ledger.Store, env config.Env) []ledger.UndoDefinition {
	return append(media.Undo(s, env),
		ledger.UndoDefinition{Operation: UndoChannelDelete, Undoer: ledger.Undoer{
			Validate: validateOp(UndoChannelDelete),
			Local: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
				value, err := undoChannelDelete(ctx, tx, raw)
				if err != nil {
					return ledger.Result{}, err
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			},
		}},
		ledger.UndoDefinition{Operation: UndoChannelRestore, Undoer: ledger.Undoer{
			Validate: validateOp(UndoChannelRestore),
			Local: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
				value, err := undoChannelRestore(ctx, tx, raw)
				if err != nil {
					return ledger.Result{}, err
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			},
		}},
	)
}
