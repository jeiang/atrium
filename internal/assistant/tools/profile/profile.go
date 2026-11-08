package profiletools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const UndoOp = "profile_restore"

type Entry struct {
	Key       string
	Value     string
	UpdatedAt float64
}

func Entries(ctx context.Context, q db.Querier) ([]Entry, error) {
	rows, err := q.QueryContext(ctx, "SELECT key, value, updated_at FROM agent_profile ORDER BY key")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Entry, error) {
		var e Entry
		err := r.Scan(&e.Key, &e.Value, &e.UpdatedAt)
		return e, err
	})
}

func Block(ctx context.Context, q db.Querier) (string, error) {
	found, err := Entries(ctx, q)
	if err != nil || len(found) == 0 {
		return "", err
	}
	lines := make([]string, len(found))
	for i, e := range found {
		lines[i] = "- " + e.Key + ": " + e.Value
	}
	return "Operator profile (preferences recorded by the operator or the assistant):\n" + strings.Join(lines, "\n"), nil
}

func current(ctx context.Context, q db.Querier, key string) (*string, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM agent_profile WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func optional(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func get(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	key, ok, err := tool.TextArg(args, "key", false)
	if err != nil {
		return nil, err
	}
	found, err := Entries(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	entries := []any{}
	for _, e := range found {
		if ok && e.Key != key {
			continue
		}
		entries = append(entries, py.Dict{{Key: "key", Value: e.Key}, {Key: "value", Value: e.Value}})
	}
	return map[string]any{"entries": entries}, nil
}

var (
	keyBound   = tool.NewBound(assistant.KeyProfileKeyChars, "")
	valueBound = tool.NewBound(assistant.KeyProfileValueChars, "")
)

func set(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	key, keyOK, err := tool.TextArg(args, "key", true)
	if err != nil {
		return nil, err
	}
	value, valueOK, err := tool.TextArg(args, "value", true)
	if err != nil {
		return nil, err
	}
	if !keyOK || !valueOK {
		return nil, tool.Error("key and value are required")
	}
	if err := keyBound.ClampText(ctx, tc.DB.Read, key, "key"); err != nil {
		return nil, err
	}
	if err := valueBound.ClampText(ctx, tc.DB.Read, value, "value"); err != nil {
		return nil, err
	}
	var previous *string
	var writeID int64
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if previous, err = current(ctx, tx, key); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx,
			"INSERT INTO agent_profile (key, value, updated_at) VALUES (?, ?, ?) "+
				"ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
			key, value, tc.Now()); err != nil {
			return err
		}
		writeID, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "profile_set", Target: "profile:" + key,
			Before: map[string]any{"value": optional(previous)},
			After:  map[string]any{"value": value},
			Undo:   profileUndo{Op: UndoOp, Key: key, Previous: previous, Applied: value},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": key, "value": value, "previous": optional(previous), "write_id": writeID}, nil
}

type profileUndo struct {
	Op       string  `json:"op"`
	Key      string  `json:"key"`
	Previous *string `json:"previous"`
	Applied  string  `json:"applied"`
}

func undo(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
	var u profileUndo
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, writes.UndoError("profile undo payload is invalid")
	}
	found, err := current(ctx, q, u.Key)
	if err != nil {
		return nil, err
	}
	if optional(found) != u.Applied {
		return nil, writes.UndoError(fmt.Sprintf("profile %s changed since this write", strconv.Quote(u.Key)))
	}
	if u.Previous == nil {
		_, err = q.ExecContext(ctx, "DELETE FROM agent_profile WHERE key = ?", u.Key)
	} else {
		_, err = q.ExecContext(ctx, "UPDATE agent_profile SET value = ? WHERE key = ?", *u.Previous, u.Key)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": u.Key, "restored": optional(u.Previous)}, nil
}

func RegisterUndoer(r *writes.Registry) { r.Register(UndoOp, undo) }

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "profile_get",
			Description: "Read the operator profile: stable preferences and facts about the operator, as key/value entries. Omit key to list every entry.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"key": {"type": "string"}}, "additionalProperties": false}`),
			Handler:     get,
			Effect:      tool.Read,
			Annotated:   true,
			Flags:       tool.Flags{PrivateData: true},
			Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Nightly},
		},
		{
			Name:        "profile_set",
			Description: "Record or overwrite one stable operator preference or fact in the profile (key/value). Logged and undoable by the operator. Keep values short and factual; never store secrets.",
			InputSchema: json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": {"key": %s, "value": %s}, "required": ["key", "value"], "additionalProperties": false}`, keyBound.TextSchema(), valueBound.TextSchema())),
			Handler:     set,
			Effect:      tool.WriteUndo, Undo: "profile_restore",
			Annotated: true,
			Contexts:  tool.Kinds{tool.Chat, tool.Nightly},
		},
	}
}
