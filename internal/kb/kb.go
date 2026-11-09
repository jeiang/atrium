package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ScopeWork     = "work"
	ScopePersonal = "personal"
	ScopeOps      = "ops"

	WriterOperator = "operator"
	WriterChat     = "chat"
	WriterDreaming = "dreaming"
	WriterWorker   = "worker"
	WriterImport   = "import"

	UndoOp = "kb_restore"
)

type Record struct {
	ID             int64      `json:"id"`
	Title          string     `json:"title"`
	Aliases        []string   `json:"aliases"`
	Body           string     `json:"body"`
	Scope          string     `json:"scope"`
	ProjectID      *int64     `json:"project_id"`
	Private        bool       `json:"private"`
	Marks          tool.Marks `json:"marks"`
	Writer         string     `json:"writer"`
	SourceThreadID *int64     `json:"source_thread_id"`
	SourceSeq      *int64     `json:"source_seq"`
	SourceRunID    *int64     `json:"source_run_id"`
	Origin         *string    `json:"origin"`
	Pinned         bool       `json:"pinned"`
	ConfirmedAt    *float64   `json:"confirmed_at"`
	RetractedAt    *float64   `json:"retracted_at"`
	ExpiresAt      *float64   `json:"expires_at"`
	Embedded       bool       `json:"embedded"`
	CreatedAt      float64    `json:"created_at"`
	UpdatedAt      float64    `json:"updated_at"`
}

func (r Record) Retracted() bool { return r.RetractedAt != nil }

func (r Record) Confirmed() bool { return r.ConfirmedAt != nil }

const columns = "r.id, r.title, r.aliases, r.body, r.scope, r.project_id, r.private, r.marks, r.writer, " +
	"r.source_thread_id, r.source_seq, r.source_run_id, r.origin, r.pinned, r.confirmed_at, r.retracted_at, " +
	"r.expires_at, r.embedded, r.created_at, r.updated_at"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Record, error) {
	var r Record
	var aliases string
	var private, pinned, embedded int64
	var marks int64
	err := row.Scan(&r.ID, &r.Title, &aliases, &r.Body, &r.Scope, &r.ProjectID, &private, &marks, &r.Writer,
		&r.SourceThreadID, &r.SourceSeq, &r.SourceRunID, &r.Origin, &pinned, &r.ConfirmedAt, &r.RetractedAt,
		&r.ExpiresAt, &embedded, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal([]byte(aliases), &r.Aliases); err != nil {
		return Record{}, fmt.Errorf("kb: record %d aliases: %w", r.ID, err)
	}
	if r.Aliases == nil {
		r.Aliases = []string{}
	}
	r.Private, r.Pinned, r.Embedded = private != 0, pinned != 0, embedded != 0
	r.Marks = tool.Marks(marks)
	return r, nil
}

func Get(ctx context.Context, q db.Querier, id int64) (*Record, error) {
	found, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM kb_records r WHERE r.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

func normalize(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

func normalizeAliases(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, alias := range list {
		alias = strings.TrimSpace(alias)
		key := normalize(alias)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, alias)
	}
	return out
}

func aliasesJSON(list []string) (string, error) {
	raw, err := json.Marshal(normalizeAliases(list))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func projectOf(project *int64) int64 {
	if project == nil {
		return 0
	}
	return *project
}

func privateClause(alias string, project int64) (string, []any) {
	return "NOT ((" + alias + ".private = 1 OR COALESCE(p.private, 0) = 1) AND (" + alias + ".project_id IS NULL OR " + alias + ".project_id != ?))", []any{project}
}

func liveWhere(alias string, project int64, now float64, scope string) (string, []any) {
	where := alias + ".retracted_at IS NULL AND (" + alias + ".expires_at IS NULL OR " + alias + ".expires_at > ?)"
	args := []any{now}
	if scope == "" {
		where += " AND " + alias + ".scope != 'ops'"
	} else {
		where += " AND " + alias + ".scope = ?"
		args = append(args, scope)
	}
	clause, pargs := privateClause(alias, project)
	return where + " AND " + clause, append(args, pargs...)
}

func fromClause() string { return " FROM kb_records r LEFT JOIN projects p ON p.id = r.project_id" }

type ReadFilter struct {
	IDs       []int64
	ProjectID int64
	Now       float64
}

func Read(ctx context.Context, q db.Querier, f ReadFilter) ([]Record, error) {
	if len(f.IDs) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(f.IDs)
	if err != nil {
		return nil, err
	}
	clause, args := privateClause("r", f.ProjectID)
	where := "(r.expires_at IS NULL OR r.expires_at > ?) AND r.scope != 'ops' AND " + clause +
		" AND r.id IN (SELECT value FROM json_each(?))"
	args = append([]any{f.Now}, append(args, string(raw))...)
	rows, err := q.QueryContext(ctx, "SELECT "+columns+fromClause()+" WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Record, error) { return scan(r) })
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Record, len(found))
	for _, r := range found {
		byID[r.ID] = r
	}
	out := make([]Record, 0, len(found))
	for _, id := range f.IDs {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func Cite(id int64) string { return "K" + fmt.Sprint(id) }

func MarksOf(records []Record) tool.Marks {
	var set tool.Marks
	for _, r := range records {
		set |= r.Marks
	}
	return set
}
