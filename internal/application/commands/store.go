package commands

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/db"
)

const (
	Header            = "Atrium-Command-Id"
	IdempotencyHeader = "Idempotency-Key"
	ReplayedHeader    = "Idempotent-Replayed"
	keyMax            = 255
)

type State string

const (
	Running     State = "running"
	Succeeded   State = "succeeded"
	Failed      State = "failed"
	Interrupted State = "interrupted"
)

type Store struct {
	db  *db.DB
	now func() time.Time
}

func New(d *db.DB) *Store { return &Store{db: d, now: time.Now} }

type record struct {
	id          string
	fingerprint string
	state       State
	status      int
	headers     http.Header
	body        []byte
	withheld    bool
}

func newID() string { return "cmd_" + strings.ToLower(rand.Text()) }

func seconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func (s *Store) admit(ctx context.Context, key, method, pattern, fingerprint string) (record, bool, error) {
	var rec record
	fresh := false
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if key != "" {
			var status sql.NullInt64
			var headers sql.NullString
			err := tx.QueryRowContext(ctx, "SELECT id, fingerprint, state, status, headers_json, body, withheld FROM commands WHERE idempotency_key = ?", key).
				Scan(&rec.id, &rec.fingerprint, &rec.state, &status, &headers, &rec.body, &rec.withheld)
			switch {
			case err == nil:
				rec.status = int(status.Int64)
				if headers.Valid {
					return json.Unmarshal([]byte(headers.String), &rec.headers)
				}
				return nil
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		rec, fresh = record{id: newID(), fingerprint: fingerprint, state: Running}, true
		var stored any
		if key != "" {
			stored = key
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO commands (id, idempotency_key, method, pattern, fingerprint, state, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			rec.id, stored, method, pattern, fingerprint, string(Running), seconds(s.now()))
		return err
	})
	return rec, fresh, err
}

func (s *Store) finish(ctx context.Context, id string, status int, headers http.Header, body []byte, withheld bool) error {
	state := Succeeded
	if status >= http.StatusBadRequest {
		state = Failed
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	if withheld {
		body = nil
	}
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE commands SET state = ?, ended_at = ?, status = ?, headers_json = ?, body = ?, withheld = ? WHERE id = ? AND state = ?",
			string(state), seconds(s.now()), status, string(raw), body, withheld, id, string(Running))
		return err
	})
}

type AuditRow struct {
	ID        int64  `json:"id"`
	Operation string `json:"operation"`
	Outcome   string `json:"outcome"`
}

type Status struct {
	CommandID string     `json:"command_id"`
	State     State      `json:"state" api:"enum=running|succeeded|failed|interrupted"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at" api:"nullable"`
	Status    *int       `json:"status" api:"nullable"`
	Audit     []AuditRow `json:"audit"`
	EffectIDs []int64    `json:"effect_ids"`
}

type StatusInput struct {
	ID string `path:"command_id" json:"-"`
}

const StatusName application.Name = "commands.status"

func DescribeStatus() application.Definition {
	return application.Describe[StatusInput, Status](StatusName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func fromSeconds(v float64) time.Time {
	return time.Unix(0, int64(v*1e9)).UTC()
}

func (s *Store) Status(ctx context.Context, in StatusInput) (application.Read[Status], error) {
	out := Status{CommandID: in.ID, Audit: []AuditRow{}, EffectIDs: []int64{}}
	var created float64
	var ended sql.NullFloat64
	var status sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx, "SELECT state, created_at, ended_at, status FROM commands WHERE id = ?", in.ID).Scan(&out.State, &created, &ended, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return application.Read[Status]{}, &application.Error{Code: application.NotFound, Message: "No such command."}
	}
	if err != nil {
		return application.Read[Status]{}, err
	}
	out.CreatedAt = fromSeconds(created)
	if ended.Valid {
		t := fromSeconds(ended.Float64)
		out.EndedAt = &t
	}
	if status.Valid {
		n := int(status.Int64)
		out.Status = &n
	}
	rows, err := s.db.Read.QueryContext(ctx, "SELECT id, operation, outcome FROM operation_audit WHERE command_id = ? ORDER BY id", in.ID)
	if err != nil {
		return application.Read[Status]{}, err
	}
	audit, err := db.Collect(rows, func(r *sql.Rows) (AuditRow, error) {
		var a AuditRow
		return a, r.Scan(&a.ID, &a.Operation, &a.Outcome)
	})
	if err != nil {
		return application.Read[Status]{}, err
	}
	out.Audit = append(out.Audit, audit...)
	rows, err = s.db.Read.QueryContext(ctx,
		"SELECT DISTINCT CAST(e.value AS INTEGER) FROM operation_audit a, json_each(a.details_json, '$.effect_ids') e WHERE a.command_id = ? ORDER BY 1", in.ID)
	if err != nil {
		return application.Read[Status]{}, err
	}
	effects, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
	if err != nil {
		return application.Read[Status]{}, err
	}
	out.EffectIDs = append(out.EffectIDs, effects...)
	rev := string(out.State) + "." + strconv.Itoa(len(out.Audit))
	return application.Read[Status]{Revision: application.Revision(rev), Data: out}, nil
}
