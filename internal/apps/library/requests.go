package library

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	RequestRequested = "requested"
	RequestWaiting   = "waiting"
	RequestFiled     = "filed"
	RequestFailed    = "failed"

	ApprovedByOperator = "operator"
	ApprovedByProposal = "proposal"

	ChannelKeyPrefix = "media:channel:"
	AutoFilePrefix   = "media:auto-file:"
	FilePrefixKey    = "media:file:"
)

type Request struct {
	ID         int64   `json:"id"`
	Hash       string  `json:"hash"`
	ThreadID   *int64  `json:"thread_id"`
	ProposalID *int64  `json:"proposal_id"`
	ApprovedBy string  `json:"approved_by"`
	ApprovedAt float64 `json:"approved_at"`
	Download   *string `json:"download"`
	EffectID   *int64  `json:"effect_id"`
	FiledBy    *string `json:"filed_by"`
	State      string  `json:"state"`
	Reason     *string `json:"reason"`
	Created    float64 `json:"created_at"`
	Updated    float64 `json:"updated_at"`
}

const requestColumns = "id, hash, thread_id, proposal_id, approved_by, approved_at, download, effect_id, filed_by, state, reason, created_at, updated_at"

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.Hash, &r.ThreadID, &r.ProposalID, &r.ApprovedBy, &r.ApprovedAt, &r.Download, &r.EffectID, &r.FiledBy, &r.State, &r.Reason, &r.Created, &r.Updated)
	return r, err
}

func RecordRequest(ctx context.Context, q db.Querier, hash string, scope contract.Scope, now float64) error {
	if scope.Actor == contract.Job || hash == "" {
		return nil
	}
	by := ApprovedByOperator
	if scope.ProposalID != nil {
		by = ApprovedByProposal
	}
	_, err := q.ExecContext(ctx, `INSERT INTO library_requests (hash, thread_id, proposal_id, approved_by, approved_at, state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(hash) DO UPDATE SET thread_id = excluded.thread_id, proposal_id = excluded.proposal_id, approved_by = excluded.approved_by,
approved_at = excluded.approved_at, updated_at = excluded.updated_at`,
		strings.ToLower(hash), scope.ThreadID, scope.ProposalID, by, now, RequestRequested, now, now)
	return err
}

func ListRequests(ctx context.Context, q db.Querier) ([]Request, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+requestColumns+" FROM library_requests ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

func RequestFor(ctx context.Context, q db.Querier, hash string) (*Request, error) {
	r, err := scanRequest(q.QueryRowContext(ctx, "SELECT "+requestColumns+" FROM library_requests WHERE hash = ?", strings.ToLower(hash)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

type RequestUpdate struct {
	State    string
	Download string
	EffectID int64
	FiledBy  string
	Reason   string
}

func UpdateRequest(ctx context.Context, q db.Querier, hash string, u RequestUpdate, now float64) error {
	_, err := q.ExecContext(ctx, `UPDATE library_requests SET state = ?, download = COALESCE(NULLIF(?, ''), download),
effect_id = COALESCE(NULLIF(?, 0), effect_id), filed_by = COALESCE(NULLIF(?, ''), filed_by), reason = NULLIF(?, ''), updated_at = ?
WHERE hash = ? AND (state <> ? OR COALESCE(reason, '') <> ? OR COALESCE(download, '') <> ? OR NULLIF(?, 0) IS NOT NULL)`,
		u.State, u.Download, u.EffectID, u.FiledBy, u.Reason, now, strings.ToLower(hash), u.State, u.Reason, u.Download, u.EffectID)
	return err
}

func Followed(ctx context.Context, store *ledger.Store, q db.Querier, hash string) (bool, error) {
	channels, err := ListChannels(ctx, q)
	if err != nil || len(channels) == 0 {
		return false, err
	}
	for _, op := range []string{contract.OpAddMagnet, contract.OpAddFile} {
		adds, err := store.List(ctx, ledger.Filter{Origin: "media", Operation: op, Direction: "forward", Target: strings.ToLower(hash), Limit: 100})
		if err != nil {
			return false, err
		}
		for _, e := range adds {
			if e.State != ledger.Done {
				continue
			}
			for _, c := range channels {
				if strings.HasPrefix(e.OperationKey, ChannelKeyPrefix+strconv.FormatInt(c.ID, 10)+":") {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func FilingAttempted(ctx context.Context, store *ledger.Store, itemID string) (bool, error) {
	found, err := store.List(ctx, ledger.Filter{Origin: "media", Operation: OpFile, Direction: "forward", Target: itemID, Limit: 1})
	return len(found) > 0, err
}

func ChannelKey(id int64, hash, request string) string {
	return ChannelKeyPrefix + strconv.FormatInt(id, 10) + ":" + hash + ":" + request
}
