package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const (
	RenewSeconds = 30
	StuckSeconds = 4 * RenewSeconds
)

type Resource struct {
	Key          string
	RetainOnDone bool
}

type New struct {
	Origin       string
	Operation    string
	OperationKey string
	Target       string
	ProposalID   *int64
	ThreadID     *int64
	RunID        *int64
	MessageID    *int64
	DecisionID   *int64
	Account      string
	Digest       string
	Src          string
	Dest         string
	Action       json.RawMessage
	Before       json.RawMessage
	Undo         json.RawMessage
	Resources    []Resource
}

type Ticket struct {
	ID    int64
	Token string
}

type Result struct {
	State  State
	Target string
	Before json.RawMessage
	After  json.RawMessage
	Undo   json.RawMessage
	Error  string
}

type Resolver struct {
	Actor  string
	Source string
}

type Resolution struct {
	Outcome  string
	Evidence json.RawMessage
	Undo     json.RawMessage
}

type Filter struct {
	Origin      string
	Operation   string
	ProposalID  *int64
	ThreadID    *int64
	RunID       *int64
	BeforeID    int64
	Limit       int
	BlockedOnly bool
}

type Mutation func(context.Context, *sql.Tx) error

type Undoer struct {
	Validate func(json.RawMessage) error
	External func(context.Context, json.RawMessage) (Result, error)
	Local    func(context.Context, *sql.Tx, json.RawMessage) (Result, error)
	Finalize func(context.Context, *sql.Tx, json.RawMessage, Result) error
}

type Store struct {
	DB      *db.DB
	now     func() float64
	mu      sync.RWMutex
	undoers map[string]Undoer
}

type Conflict struct{ Resource string }

func (e *Conflict) Error() string { return "the resource is already claimed: " + e.Resource }

type Refused string

func (e Refused) Error() string { return string(e) }

type SettledMeanwhile struct{ ID int64 }

func (e *SettledMeanwhile) Error() string {
	return fmt.Sprintf("effect %d was settled or resolved while the attempt was in flight", e.ID)
}

type NotFound struct{ ID int64 }

func (e *NotFound) Error() string { return fmt.Sprintf("no effect %d", e.ID) }

func NewStore(d *db.DB, now func() float64) *Store {
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	return &Store{DB: d, now: now, undoers: map[string]Undoer{}}
}

func (s *Store) Now() float64 { return s.now() }

func (s *Store) RegisterUndo(name string, fn Undoer) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("ledger: undo registration without a name")
	}
	if (fn.External == nil) == (fn.Local == nil) {
		return fmt.Errorf("ledger: undo %q must provide exactly one of External or Local", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.undoers[name] = fn
	return nil
}

func (s *Store) undoer(name string) (Undoer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn, ok := s.undoers[name]
	return fn, ok
}

func NewKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) Begin(ctx context.Context, n New, admit Mutation) (Ticket, error) {
	if err := ctx.Err(); err != nil {
		return Ticket{}, err
	}
	if err := validateNew(n); err != nil {
		return Ticket{}, err
	}
	if err := s.validateUndo(n.Undo); err != nil {
		return Ticket{}, err
	}
	token, err := newToken()
	if err != nil {
		return Ticket{}, err
	}
	now := s.now()
	var id int64
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if admit != nil {
			if err := admit(ctx, tx); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, insertEffect,
			now, now, token, n.Origin, n.Operation, n.OperationKey, "forward", nil,
			n.ProposalID, n.ThreadID, n.RunID, n.Account, n.MessageID, n.Digest, n.DecisionID,
			nullText(n.Src), nullText(n.Dest), n.Target, actionText(n.Action), rawText(n.Before), nil, rawText(n.Undo),
			string(Intent), nil, nil)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return claim(ctx, tx, id, n.Resources)
	})
	if err != nil {
		return Ticket{}, err
	}
	return Ticket{ID: id, Token: token}, nil
}

func (s *Store) Heartbeat(ctx context.Context, t Ticket) error {
	res, err := s.DB.Write.ExecContext(ctx,
		"UPDATE effects SET lease_at = ? WHERE id = ? AND token = ? AND state = 'intent' "+
			"AND NOT EXISTS (SELECT 1 FROM effect_resolutions r WHERE r.effect_id = effects.id)", s.now(), t.ID, t.Token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return &SettledMeanwhile{ID: t.ID}
	}
	return nil
}

func (s *Store) Settle(ctx context.Context, t Ticket, r Result, local Mutation) error {
	ctx = context.WithoutCancel(ctx)
	if !r.State.Final() {
		return fmt.Errorf("ledger: effect %d cannot settle as %q", t.ID, r.State)
	}
	if err := s.validateUndo(r.Undo); err != nil {
		return err
	}
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var direction string
		if err := tx.QueryRowContext(ctx, "SELECT direction FROM effects WHERE id = ? AND token = ?", t.ID, t.Token).
			Scan(&direction); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &SettledMeanwhile{ID: t.ID}
			}
			return err
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE effects SET state = ?, settled_at = ?, target = COALESCE(?, target), "+
				"before_json = COALESCE(?, before_json), after_json = COALESCE(?, after_json), "+
				"undo_json = COALESCE(?, undo_json), error = ? "+
				"WHERE id = ? AND token = ? AND state = 'intent' "+
				"AND NOT EXISTS (SELECT 1 FROM effect_resolutions r WHERE r.effect_id = effects.id)",
			string(r.State), s.now(), nullText(r.Target), rawText(r.Before), rawText(r.After), rawText(r.Undo), nullText(r.Error), t.ID, t.Token)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &SettledMeanwhile{ID: t.ID}
		}
		if local != nil {
			if err := local(ctx, tx); err != nil {
				return err
			}
		}
		return s.afterTransition(ctx, tx, t.ID, direction, r.State)
	})
	return err
}

func (s *Store) ApplyLocal(ctx context.Context, n New, apply func(context.Context, *sql.Tx) (Result, error)) (Effect, error) {
	if err := ctx.Err(); err != nil {
		return Effect{}, err
	}
	if err := validateNew(n); err != nil {
		return Effect{}, err
	}
	if err := s.validateUndo(n.Undo); err != nil {
		return Effect{}, err
	}
	token, err := newToken()
	if err != nil {
		return Effect{}, err
	}
	now := s.now()
	var id int64
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, insertEffect,
			now, now, token, n.Origin, n.Operation, n.OperationKey, "forward", nil,
			n.ProposalID, n.ThreadID, n.RunID, n.Account, n.MessageID, n.Digest, n.DecisionID,
			nullText(n.Src), nullText(n.Dest), n.Target, actionText(n.Action), rawText(n.Before), nil, rawText(n.Undo),
			string(Intent), nil, nil)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if err := claim(ctx, tx, id, n.Resources); err != nil {
			return err
		}
		r, err := apply(ctx, tx)
		if err != nil {
			return err
		}
		if !r.State.Final() {
			return fmt.Errorf("ledger: effect %d cannot settle as %q", id, r.State)
		}
		if err := s.validateUndo(r.Undo); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx,
			"UPDATE effects SET state = ?, settled_at = ?, target = COALESCE(?, target), "+
				"before_json = COALESCE(?, before_json), after_json = COALESCE(?, after_json), "+
				"undo_json = COALESCE(?, undo_json), error = ? "+
				"WHERE id = ? AND token = ? AND state = 'intent'",
			string(r.State), s.now(), nullText(r.Target), rawText(r.Before), rawText(r.After), rawText(r.Undo), nullText(r.Error), id, token)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &SettledMeanwhile{ID: id}
		}
		return s.afterTransition(ctx, tx, id, "forward", r.State)
	})
	if err != nil {
		return Effect{}, err
	}
	return s.Get(context.WithoutCancel(ctx), id)
}

func (s *Store) Recover(ctx context.Context, before float64) (int64, error) {
	res, err := s.DB.Write.ExecContext(ctx,
		"UPDATE effects SET state = 'unknown', settled_at = ?, "+
			"error = COALESCE(error, 'the attempt was interrupted; the mutation may have happened') "+
			"WHERE state = 'intent' AND lease_at < ?", s.now(), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) validateUndo(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return fmt.Errorf("ledger: undo payload: %w", err)
	}
	if head.Op == "" {
		return errors.New("ledger: undo payload without an op")
	}
	fn, ok := s.undoer(head.Op)
	if !ok {
		return Refused("unknown undo kind " + fmt.Sprintf("%q", head.Op))
	}
	if fn.Validate != nil {
		return fn.Validate(raw)
	}
	return nil
}

func (s *Store) afterTransition(ctx context.Context, tx *sql.Tx, id int64, direction string, state State) error {
	switch {
	case direction == "undo" && state == Done:
		return completeUndo(ctx, tx, id)
	case state == Done:
		return release(ctx, tx, id, false)
	case state == Failed || state == Skipped:
		return release(ctx, tx, id, true)
	}
	return nil
}

func validateNew(n New) error {
	for _, raw := range []json.RawMessage{n.Action, n.Before, n.Undo} {
		if len(raw) > 0 && !json.Valid(raw) {
			return errors.New("ledger: an effect payload is not valid JSON")
		}
	}
	if strings.TrimSpace(n.Origin) == "" {
		return errors.New("ledger: an effect needs an origin")
	}
	if strings.TrimSpace(n.Operation) == "" {
		return errors.New("ledger: an effect needs an operation")
	}
	if strings.TrimSpace(n.OperationKey) == "" {
		return errors.New("ledger: an effect needs an operation key")
	}
	if strings.TrimSpace(n.Target) == "" {
		return errors.New("ledger: an effect needs a target")
	}
	return nil
}

const insertEffect = "INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, undo_of, " +
	"proposal_id, thread_id, run_id, account, message_id, digest, decision_id, src, dest, target, action_json, before_json, after_json, " +
	"undo_json, state, settled_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

func claim(ctx context.Context, tx *sql.Tx, effectID int64, resources []Resource) error {
	seen := map[string]Resource{}
	for _, r := range resources {
		if strings.TrimSpace(r.Key) == "" {
			return errors.New("ledger: an effect resource has no key")
		}
		if prior, ok := seen[r.Key]; ok {
			r.RetainOnDone = r.RetainOnDone || prior.RetainOnDone
		}
		seen[r.Key] = r
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		res, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO effect_claims (resource) VALUES (?)", key)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &Conflict{Resource: key}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO effect_resources (effect_id, resource, holding, retain_on_done) VALUES (?, ?, 1, ?)",
			effectID, key, boolInt(seen[key].RetainOnDone)); err != nil {
			return err
		}
	}
	return nil
}

func release(ctx context.Context, tx *sql.Tx, effectID int64, all bool) error {
	keys, err := effectKeys(ctx, tx, effectID)
	if err != nil {
		return err
	}
	query := "UPDATE effect_resources SET holding = 0 WHERE effect_id = ?"
	if !all {
		query += " AND retain_on_done = 0"
	}
	if _, err := tx.ExecContext(ctx, query, effectID); err != nil {
		return err
	}
	return deleteOrphanClaims(ctx, tx, keys)
}

func completeUndo(ctx context.Context, tx *sql.Tx, undoID int64) error {
	var forwardID int64
	if err := tx.QueryRowContext(ctx, "SELECT undo_of FROM effects WHERE id = ?", undoID).Scan(&forwardID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE effects SET undone_by = ? WHERE id = ? AND undone_by IS NULL", undoID, forwardID); err != nil {
		return err
	}
	keys, err := effectKeys(ctx, tx, forwardID)
	if err != nil {
		return err
	}
	undoKeys, err := effectKeys(ctx, tx, undoID)
	if err != nil {
		return err
	}
	keys = append(keys, undoKeys...)
	if _, err := tx.ExecContext(ctx, "UPDATE effect_resources SET holding = 0 WHERE effect_id IN (?, ?)", forwardID, undoID); err != nil {
		return err
	}
	return deleteOrphanClaims(ctx, tx, keys)
}

func effectKeys(ctx context.Context, q db.Querier, effectID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT resource FROM effect_resources WHERE effect_id = ?", effectID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var key string
		return key, r.Scan(&key)
	})
}

func deleteOrphanClaims(ctx context.Context, tx *sql.Tx, keys []string) error {
	seen := map[string]struct{}{}
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM effect_claims WHERE resource = ? AND NOT EXISTS "+
				"(SELECT 1 FROM effect_resources WHERE resource = ? AND holding = 1)", key, key); err != nil {
			return err
		}
	}
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func rawText(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	return string(v)
}

func actionText(v json.RawMessage) string {
	if len(v) == 0 {
		return "{}"
	}
	return string(v)
}
