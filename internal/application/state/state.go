package state

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	Knowledge   = "knowledge"
	Notion      = "notion"
	Tasks       = "tasks"
	Review      = "review"
	Effects     = "effects"
	Calendar    = "calendar"
	Assistant   = "assistant"
	Jobs        = "jobs"
	Media       = "media"
	Mail        = "mail"
	Settings    = "settings"
	Connections = "connections"
	Notify      = "notify"
	Attention   = "attention"
	Ops         = "ops"
	Auth        = "auth"
)

var scopes = []string{Knowledge, Notion, Tasks, Review, Effects, Calendar, Assistant, Jobs, Media, Mail, Settings, Connections, Notify, Attention, Ops, Auth}

type Outcome string

const (
	OK       Outcome = "ok"
	Noop     Outcome = "noop"
	Invalid  Outcome = "invalid"
	Refused  Outcome = "refused"
	Conflict Outcome = "conflict"
	NotFound Outcome = "not_found"
	Failed   Outcome = "failed"
	Unknown  Outcome = "unknown"
	Partial  Outcome = "partial"
	Canceled Outcome = "canceled"
	DryRun   Outcome = "dry_run"
)

func (o Outcome) Valid() bool {
	switch o {
	case OK, Noop, Invalid, Refused, Conflict, NotFound, Failed, Unknown, Partial, Canceled, DryRun:
		return true
	}
	return false
}

var epoch = newEpoch()

func newEpoch() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type Counter struct {
	Scope string
	Value int64
}

type Entry struct {
	TS          time.Time
	Operation   string
	Actor       string
	Source      audit.Source
	ThreadID    int64
	RunID       int64
	JobRunID    int64
	Outcome     Outcome
	Target      string
	DetailsJSON string
	CommandID   string
}

type Store struct {
	db *db.DB
}

func New(d *db.DB) *Store {
	return &Store{db: d}
}

func (s *Store) Read(ctx context.Context, q db.Querier, want []string) ([]Counter, error) {
	if q == nil {
		q = s.db.Read
	}
	names := slices.Clone(want)
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) == 0 {
		return nil, errors.New("state: a read names no revision scope")
	}
	args := make([]any, len(names))
	for i, name := range names {
		if !slices.Contains(scopes, name) {
			return nil, fmt.Errorf("state: unknown revision scope %q", name)
		}
		args[i] = name
	}
	rows, err := q.QueryContext(ctx,
		"SELECT scope, value FROM read_revisions WHERE scope IN ("+strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")+")", args...)
	if err != nil {
		return nil, err
	}
	values, err := db.Collect(rows, func(r *sql.Rows) (Counter, error) {
		var c Counter
		err := r.Scan(&c.Scope, &c.Value)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Counter, len(names))
	for i, name := range names {
		out[i] = Counter{Scope: name}
		for _, v := range values {
			if v.Scope == name {
				out[i].Value = v.Value
			}
		}
	}
	return out, nil
}

func (s *Store) Audit(ctx context.Context, tx *sql.Tx, e Entry) (int64, error) {
	if e.Actor == "" {
		e.Actor = audit.Actor(ctx)
	}
	if e.Source == "" {
		e.Source = audit.SourceOf(ctx)
	}
	if e.DetailsJSON == "" {
		e.DetailsJSON = "{}"
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	switch {
	case strings.TrimSpace(e.Operation) == "":
		return 0, errors.New("state: an operation audit names no operation")
	case strings.TrimSpace(e.Actor) == "":
		return 0, errors.New("state: an operation audit names no actor")
	case !e.Source.Valid():
		return 0, fmt.Errorf("state: unknown audit source %q", e.Source)
	case !e.Outcome.Valid():
		return 0, fmt.Errorf("state: unknown audit outcome %q", e.Outcome)
	}
	var details map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.DetailsJSON), &details); err != nil || details == nil {
		return 0, errors.New("state: operation audit details must be a JSON object")
	}
	commandID := sql.NullString{String: e.CommandID, Valid: e.CommandID != ""}
	if inv, ok := application.InvocationOf(ctx); ok && !commandID.Valid && inv.CommandID != "" {
		commandID = sql.NullString{String: inv.CommandID, Valid: true}
	}
	var id int64
	insert := func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO operation_audit (ts, operation, actor, source, thread_id, run_id, job_run_id, outcome, target, details_json, command_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			float64(e.TS.UnixNano())/1e9, e.Operation, e.Actor, string(e.Source), nullID(e.ThreadID), nullID(e.RunID), nullID(e.JobRunID),
			string(e.Outcome), e.Target, e.DetailsJSON, commandID)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}
	run := s.db.WithTx
	if tx != nil {
		run = func(_ context.Context, fn func(*sql.Tx) error) error { return fn(tx) }
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	if err := run(ctx, insert); err != nil {
		return 0, err
	}
	return id, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func Revision(projection string, counters []Counter, input any, visibility string) string {
	sorted := slices.Clone(counters)
	slices.SortFunc(sorted, func(a, b Counter) int { return strings.Compare(a.Scope, b.Scope) })
	normalized, err := json.Marshal(input)
	if err != nil {
		normalized = []byte(fmt.Sprintf("%#v", input))
	}
	var b strings.Builder
	for _, part := range []string{epoch, projection, visibility, string(normalized)} {
		b.WriteString(strconv.Itoa(len(part)) + ":" + part)
	}
	for _, c := range sorted {
		b.WriteString(c.Scope + "=" + strconv.FormatInt(c.Value, 10) + ";")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}
