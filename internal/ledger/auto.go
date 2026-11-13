package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
)

const AutoOrigin = "mail_auto"

const LegacyAutoUndo = "legacy_auto_undo"

func AutoKey(messageID int64, kind, dest string) string {
	return fmt.Sprintf("auto/%d/%s/%s", messageID, kind, dest)
}

func MailResource(account, imapName string, validity, uid uint32) string {
	return fmt.Sprintf("mail/%s/%s/%d/%d", account, imapName, validity, uid)
}

func digestKey(digest, kind string) string {
	return digest + "\x00" + kind
}

type AutoStates struct {
	held         map[string]bool
	undone       map[string]bool
	failed       map[string]int
	digestHeld   map[string]bool
	digestUndone map[string]bool
	digestFailed map[string]int
}

func (s AutoStates) Blocked(messageID int64, kind, dest string) bool {
	return s.held[AutoKey(messageID, kind, dest)]
}

func (s AutoStates) Undone(messageID int64, kind, dest string) bool {
	return s.undone[AutoKey(messageID, kind, dest)]
}

func (s AutoStates) DigestUndone(digest string, kinds ...string) bool {
	for _, kind := range kinds {
		if s.digestUndone[digestKey(digest, kind)] {
			return true
		}
	}
	return false
}

func (s AutoStates) Failures(messageID int64, kind, dest string) int {
	return s.failed[AutoKey(messageID, kind, dest)]
}

func (s AutoStates) DigestBlocked(digest string, kinds ...string) bool {
	for _, kind := range kinds {
		if s.digestHeld[digestKey(digest, kind)] {
			return true
		}
	}
	return false
}

func (s AutoStates) DigestFailures(digest string, kinds ...string) int {
	total := 0
	for _, kind := range kinds {
		total += s.digestFailed[digestKey(digest, kind)]
	}
	return total
}

func AutoStatesFor(ctx context.Context, q db.Querier, account string) (AutoStates, error) {
	states := AutoStates{
		held: map[string]bool{}, undone: map[string]bool{}, failed: map[string]int{},
		digestHeld: map[string]bool{}, digestUndone: map[string]bool{}, digestFailed: map[string]int{},
	}
	rows, err := q.QueryContext(ctx,
		"SELECT DISTINCT e.message_id, e.operation, COALESCE(e.dest, ''), e.digest FROM effects e "+
			"JOIN effect_resources er ON er.effect_id = e.id "+
			"WHERE e.origin = ? AND e.direction = 'forward' AND e.account = ? AND er.holding = 1",
		AutoOrigin, account)
	if err != nil {
		return states, err
	}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var messageID sql.NullInt64
		var kind, dest, digest string
		if err := r.Scan(&messageID, &kind, &dest, &digest); err != nil {
			return struct{}{}, err
		}
		if messageID.Valid {
			states.held[AutoKey(messageID.Int64, kind, dest)] = true
		}
		states.digestHeld[digestKey(digest, kind)] = true
		return struct{}{}, nil
	}); err != nil {
		return states, err
	}
	rows, err = q.QueryContext(ctx,
		"SELECT e.message_id, e.operation, COALESCE(e.dest, ''), e.digest FROM effects e "+
			"WHERE e.origin = ? AND e.direction = 'forward' AND e.account = ? AND e.undone_by IS NOT NULL",
		AutoOrigin, account)
	if err != nil {
		return states, err
	}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var messageID sql.NullInt64
		var kind, dest, digest string
		if err := r.Scan(&messageID, &kind, &dest, &digest); err != nil {
			return struct{}{}, err
		}
		if messageID.Valid {
			states.undone[AutoKey(messageID.Int64, kind, dest)] = true
		}
		states.digestUndone[digestKey(digest, kind)] = true
		return struct{}{}, nil
	}); err != nil {
		return states, err
	}
	rows, err = q.QueryContext(ctx,
		"SELECT e.message_id, e.operation, COALESCE(e.dest, ''), e.digest, COUNT(*) FROM effects e "+
			"LEFT JOIN effect_resolutions r ON r.id = (SELECT MAX(id) FROM effect_resolutions WHERE effect_id = e.id) "+
			"WHERE e.origin = ? AND e.direction = 'forward' AND e.account = ? "+
			"AND (e.state = 'failed' OR r.effective_outcome = 'failed') "+
			"GROUP BY e.message_id, e.operation, COALESCE(e.dest, ''), e.digest",
		AutoOrigin, account)
	if err != nil {
		return states, err
	}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var messageID sql.NullInt64
		var kind, dest, digest string
		var n int
		if err := r.Scan(&messageID, &kind, &dest, &digest, &n); err != nil {
			return struct{}{}, err
		}
		if messageID.Valid {
			states.failed[AutoKey(messageID.Int64, kind, dest)] += n
		}
		states.digestFailed[digestKey(digest, kind)] += n
		return struct{}{}, nil
	}); err != nil {
		return states, err
	}
	return states, nil
}

func PriorAutoDest(ctx context.Context, q db.Querier, account, digest string) (*string, error) {
	var dest string
	err := q.QueryRowContext(ctx,
		"SELECT e.dest FROM effects e "+
			"LEFT JOIN effect_resolutions r ON r.id = (SELECT MAX(id) FROM effect_resolutions WHERE effect_id = e.id) "+
			"WHERE e.origin = ? AND e.direction = 'forward' AND e.account = ? AND e.digest = ? "+
			"AND e.operation IN ('file', 'correct') AND e.undone_by IS NULL AND e.dest IS NOT NULL "+
			"AND (e.state = 'done' OR r.effective_outcome = 'done') ORDER BY e.id DESC LIMIT 1",
		AutoOrigin, account, digest).Scan(&dest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dest, nil
}

func AutoCounts(ctx context.Context, q db.Querier) (map[string]map[string]int64, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT operation, state, COUNT(*) FROM effects WHERE origin = ? AND direction = 'forward' GROUP BY 1, 2", AutoOrigin)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int64{}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var operation string
		var state State
		var n int64
		if err := r.Scan(&operation, &state, &n); err != nil {
			return struct{}{}, err
		}
		if out[operation] == nil {
			out[operation] = map[string]int64{}
		}
		out[operation][string(state)] += n
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func AutoForMessages(ctx context.Context, q db.Querier, ids []int64, now float64) (map[int64]Effect, error) {
	out := map[int64]Effect{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	args = append(args, AutoOrigin)
	rows, err := q.QueryContext(ctx, effectSelect+
		" WHERE e.message_id IN ("+placeholders(len(ids))+") AND e.origin = ? AND e.direction = 'forward' "+
		"AND e.operation IN ('file', 'trash', 'correct') "+
		"AND e.id = (SELECT MAX(y.id) FROM effects y WHERE y.message_id = e.message_id AND y.origin = ? "+
		"AND y.direction = 'forward' AND y.operation IN ('file', 'trash', 'correct'))", append(args, AutoOrigin)...)
	if err != nil {
		return nil, err
	}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		e, err := scanEffect(r)
		if err != nil {
			return struct{}{}, err
		}
		e.UndoBlocked = undoBlocked(e, now)
		if e.MessageID != nil {
			out[*e.MessageID] = e
		}
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func LastEffectID(ctx context.Context, q db.Querier) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM effects").Scan(&id)
	return id, err
}

func AutoEffectsSince(ctx context.Context, q db.Querier, messageID, after int64) (ids []int64, unknown bool, err error) {
	rows, err := q.QueryContext(ctx, "SELECT id, state FROM effects WHERE id > ? AND message_id = ? AND origin = ? ORDER BY id", after, messageID, AutoOrigin)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var st State
		if err := rows.Scan(&id, &st); err != nil {
			return ids, unknown, err
		}
		ids = append(ids, id)
		unknown = unknown || st == Unknown
	}
	return ids, unknown, rows.Err()
}
