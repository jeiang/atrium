package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	TargetRecord  = "record"
	TargetTask    = "task"
	TargetContact = "contact"
	TargetSpace   = "space"

	RelationAbout     = "about"
	RelationPartOf    = "part_of"
	RelationWorksWith = "works_with"
	RelationDependsOn = "depends_on"
	RelationSeeAlso   = "see_also"

	LinkerOperator  = "operator"
	LinkerAssistant = "assistant"

	LinkUndoOp = "kb_link_restore"

	linkColumns = "id, record_id, target_kind, target_id, relation, writer, run_id, created_at, retracted_at"
)

var (
	LinkTargets = []string{TargetRecord, TargetTask, TargetContact, TargetSpace}
	Relations   = []string{RelationAbout, RelationPartOf, RelationWorksWith, RelationDependsOn, RelationSeeAlso}
)

type Link struct {
	ID          int64
	RecordID    int64
	TargetKind  string
	TargetID    string
	Relation    string
	Writer      string
	RunID       *int64
	CreatedAt   float64
	RetractedAt *float64
}

func (l Link) Retracted() bool { return l.RetractedAt != nil }

type LinkInput struct {
	RecordID   int64
	TargetKind string
	TargetID   string
	Relation   string
	Writer     string
	RunID      *int64
}

type linkUndo struct {
	Op      string   `json:"op"`
	ID      int64    `json:"id"`
	Retract bool     `json:"retract"`
	Applied *float64 `json:"applied_retracted_at"`
}

func scanLink(r interface{ Scan(...any) error }) (Link, error) {
	var l Link
	err := r.Scan(&l.ID, &l.RecordID, &l.TargetKind, &l.TargetID, &l.Relation, &l.Writer, &l.RunID, &l.CreatedAt, &l.RetractedAt)
	return l, err
}

func links(ctx context.Context, q db.Querier, where string, args ...any) ([]Link, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+linkColumns+" FROM record_links WHERE retracted_at IS NULL AND "+where+" ORDER BY created_at, id", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Link, error) { return scanLink(r) })
}

func LinksFrom(ctx context.Context, q db.Querier, id int64) ([]Link, error) {
	return links(ctx, q, "record_id = ?", id)
}

func LinksTo(ctx context.Context, q db.Querier, kind, id string) ([]Link, error) {
	return links(ctx, q, "target_kind = ? AND target_id = ?", kind, id)
}

func RecordLinks(ctx context.Context, q db.Querier) ([]Link, error) {
	return links(ctx, q, "target_kind = ?", TargetRecord)
}

func GetLink(ctx context.Context, q db.Querier, id int64) (*Link, error) {
	l, err := scanLink(q.QueryRowContext(ctx, "SELECT "+linkColumns+" FROM record_links WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func LiveLink(ctx context.Context, q db.Querier, in LinkInput) (*Link, error) {
	found, err := links(ctx, q, "record_id = ? AND target_kind = ? AND target_id = ? AND relation = ?", in.RecordID, in.TargetKind, in.TargetID, in.Relation)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

func RecordTarget(id string) (int64, bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	return n, err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func linkResult(l Link) ledger.Result {
	state := map[string]any{"link_id": l.ID, "target_kind": l.TargetKind, "target_id": l.TargetID, "relation": l.Relation, "retracted_at": l.RetractedAt}
	return ledger.Result{State: ledger.Done, Target: "kb:" + fmt.Sprint(l.RecordID), After: jsonOf(state),
		Undo: jsonOf(linkUndo{Op: LinkUndoOp, ID: l.ID, Retract: !l.Retracted(), Applied: l.RetractedAt})}
}

func LinkTx(ctx context.Context, tx *sql.Tx, in LinkInput, now float64) (Link, ledger.Result, error) {
	if !slices.Contains(LinkTargets, in.TargetKind) || !slices.Contains(Relations, in.Relation) || !slices.Contains([]string{LinkerOperator, LinkerAssistant}, in.Writer) {
		return Link{}, ledger.Result{}, fmt.Errorf("knowledge link %s %s by %s is not allowed", in.TargetKind, in.Relation, in.Writer)
	}
	src, err := Load(ctx, tx, in.RecordID)
	if err != nil {
		return Link{}, ledger.Result{}, err
	}
	if src.Retracted() {
		return Link{}, ledger.Result{}, Refused(Cite(src.ID) + " is retracted")
	}
	if in.TargetKind == TargetRecord {
		id, ok := RecordTarget(in.TargetID)
		if !ok {
			return Link{}, ledger.Result{}, Refused("a record target is a record id")
		}
		if id == in.RecordID {
			return Link{}, ledger.Result{}, Refused(Cite(id) + " cannot link to itself")
		}
		target, err := Load(ctx, tx, id)
		if err != nil {
			return Link{}, ledger.Result{}, err
		}
		if target.Retracted() {
			return Link{}, ledger.Result{}, Refused(Cite(id) + " is retracted")
		}
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO record_links (record_id, target_kind, target_id, relation, writer, run_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		in.RecordID, in.TargetKind, in.TargetID, in.Relation, in.Writer, in.RunID, now)
	if err != nil {
		return Link{}, ledger.Result{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Link{}, ledger.Result{}, err
	}
	l := Link{ID: id, RecordID: in.RecordID, TargetKind: in.TargetKind, TargetID: in.TargetID, Relation: in.Relation, Writer: in.Writer, RunID: in.RunID, CreatedAt: now}
	return l, linkResult(l), nil
}

func UnlinkTx(ctx context.Context, tx *sql.Tx, id int64, now float64) (Link, ledger.Result, error) {
	l, err := GetLink(ctx, tx, id)
	if err != nil {
		return Link{}, ledger.Result{}, err
	}
	if l == nil {
		return Link{}, ledger.Result{}, Refused(fmt.Sprintf("link %d is unknown", id))
	}
	if _, err := tx.ExecContext(ctx, "UPDATE record_links SET retracted_at = ? WHERE id = ? AND retracted_at IS NULL", now, id); err != nil {
		return Link{}, ledger.Result{}, err
	}
	l.RetractedAt = &now
	return *l, linkResult(*l), nil
}

func undoLink(ctx context.Context, tx *sql.Tx, payload json.RawMessage) (any, error) {
	var u linkUndo
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, ledger.Refused("knowledge link undo payload is invalid")
	}
	l, err := GetLink(ctx, tx, u.ID)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, ledger.Refused(fmt.Sprintf("link %d is gone", u.ID))
	}
	if u.Retract {
		if l.Retracted() {
			return nil, ledger.Refused(fmt.Sprintf("link %d is already removed", u.ID))
		}
		now := float64(time.Now().UnixNano()) / 1e9
		if _, err := tx.ExecContext(ctx, "UPDATE record_links SET retracted_at = ? WHERE id = ?", now, u.ID); err != nil {
			return nil, err
		}
		return map[string]any{"link_id": u.ID, "retracted": true}, nil
	}
	if l.RetractedAt == nil || u.Applied == nil || *l.RetractedAt != *u.Applied {
		return nil, ledger.Refused(fmt.Sprintf("link %d changed since this write", u.ID))
	}
	live, err := LiveLink(ctx, tx, LinkInput{RecordID: l.RecordID, TargetKind: l.TargetKind, TargetID: l.TargetID, Relation: l.Relation})
	if err != nil {
		return nil, err
	}
	if live != nil {
		return nil, ledger.Refused(fmt.Sprintf("link %d was added again", u.ID))
	}
	src, err := Get(ctx, tx, l.RecordID)
	if err != nil {
		return nil, err
	}
	if src == nil || src.Retracted() {
		return nil, ledger.Refused(Cite(l.RecordID) + " is retracted")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE record_links SET retracted_at = NULL WHERE id = ?", u.ID); err != nil {
		return nil, err
	}
	return map[string]any{"link_id": u.ID, "restored": true}, nil
}

func linkUndoDefinition() ledger.UndoDefinition {
	return ledger.UndoDefinition{Operation: LinkUndoOp, Undoer: ledger.Undoer{
		Validate: func(payload json.RawMessage) error {
			var u linkUndo
			if err := json.Unmarshal(payload, &u); err != nil {
				return ledger.Refused("knowledge link undo payload is invalid")
			}
			if u.Op != LinkUndoOp || u.ID == 0 {
				return ledger.Refused("knowledge link undo payload names " + u.Op)
			}
			return nil
		},
		Local: func(ctx context.Context, tx *sql.Tx, payload json.RawMessage) (ledger.Result, error) {
			value, err := undoLink(ctx, tx, payload)
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
		},
	}}
}
