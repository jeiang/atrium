package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	Chat     = "chat"
	Cron     = "cron"
	Nightly  = "nightly"
	Sub      = "sub"
	Subagent = "subagent"
)

const (
	Low    = "low"
	Medium = "medium"
	High   = "high"
	XHigh  = "xhigh"
	Max    = "max"
)

var Efforts = [...]string{Low, Medium, High, XHigh, Max}

var effortKeys = map[string]string{
	Cron:     assistant.KeyEffortCron,
	Nightly:  assistant.KeyEffortNightly,
	Subagent: assistant.KeyEffortSubagent,
	Sub:      assistant.KeyEffortSub,
}

func EffortOf(ctx context.Context, q db.Querier, kind string) (string, bool, error) {
	if kind == Chat {
		return Medium, true, nil
	}
	key, ok := effortKeys[kind]
	if !ok {
		return "", false, nil
	}
	found, err := settings.Default.Resolve(ctx, q, key, 0)
	if err != nil {
		return "", true, err
	}
	effort, err := CleanEffort(found.Value)
	return effort, true, err
}

var ContextLimits = [...]int64{90_000, 200_000, 500_000, 1_000_000}

const (
	DefaultContextLimit = 90_000
	TitleChars          = 80
	DefaultListLimit    = 50
)

var urlRe = rx.MustCompile(`https?://[^\s<>\"'()\[\]]+`, 0)

type OptionError string

func (e OptionError) Error() string { return string(e) }

func CleanEffort(value string) (string, error) {
	if !slices.Contains(Efforts[:], value) {
		return "", OptionError("effort must be one of " + strings.Join(Efforts[:], ", "))
	}
	return value, nil
}

func CleanModel(value string) (pricing.Model, error) {
	m, err := pricing.Lookup(value)
	if err != nil {
		return pricing.Model{}, OptionError("model must be one of " + strings.Join(pricing.IDs(), ", "))
	}
	return m, nil
}

func CleanModelEffort(model pricing.Model, value string) (string, error) {
	effort, err := CleanEffort(value)
	if err != nil {
		return "", err
	}
	if !model.SupportsEffort(effort) {
		return "", OptionError(model.Name + " does not support effort " + effort)
	}
	return effort, nil
}

func SnapEffort(model pricing.Model, effort string) string {
	if model.SupportsEffort(effort) || len(model.Efforts) == 0 {
		return effort
	}
	if model.SupportsEffort(Medium) {
		return Medium
	}
	return model.Efforts[0]
}

func SnapContextLimit(model pricing.Model, limit int64) int64 {
	if limit <= model.ContextWindow {
		return limit
	}
	best := int64(0)
	for _, n := range ContextLimits {
		if n <= model.ContextWindow && n > best {
			best = n
		}
	}
	if DefaultContextLimit <= model.ContextWindow {
		return DefaultContextLimit
	}
	return best
}

func CleanModelContextLimit(model pricing.Model, value any) (int64, error) {
	limit, err := CleanContextLimit(value)
	if err != nil {
		return 0, err
	}
	if limit > model.ContextWindow {
		return 0, OptionError(fmt.Sprintf("%s supports at most %dK of context", model.Name, model.ContextWindow/1000))
	}
	return limit, nil
}

func CleanContextLimit(value any) (int64, error) {
	limit, ok := int64(0), false
	switch v := value.(type) {
	case int:
		limit, ok = int64(v), true
	case int64:
		limit, ok = v, true
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v > -1e18 && v < 1e18 {
			limit, ok = int64(v), true
		}
	case json.Number:
		limit, ok = py.ParseInt(string(v))
	case string:
		limit, ok = py.ParseInt(v)
	}
	if ok && slices.Contains(ContextLimits[:], limit) {
		return limit, nil
	}
	sizes := make([]string, len(ContextLimits))
	for i, n := range ContextLimits {
		sizes[i] = fmt.Sprintf("%dK", n/1000)
	}
	return 0, OptionError("context limit must be one of " + strings.Join(sizes, ", "))
}

type Options struct {
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	ContextLimit int64  `json:"context_limit"`
}

func Defaults(ctx context.Context, q db.Querier) (Options, error) {
	found, err := settings.Default.Resolve(ctx, q, assistant.KeyDefaultEffort, 0)
	if err != nil {
		return Options{}, err
	}
	effort := found.Value
	if !slices.Contains(Efforts[:], effort) {
		effort = Medium
	}
	out := Options{Model: pricing.DefaultModel, Effort: effort, ContextLimit: DefaultContextLimit}
	found, err = settings.Default.Resolve(ctx, q, assistant.KeyDefaultModel, 0)
	if err != nil {
		return out, err
	}
	if _, err := CleanModel(found.Value); err != nil {
		return out, fmt.Errorf("threads: setting %s: %w", assistant.KeyDefaultModel, err)
	}
	out.Model = found.Value
	found, err = settings.Default.Resolve(ctx, q, assistant.KeyDefaultContextLimit, 0)
	if err != nil {
		return out, err
	}
	limit := found.Value
	if limit == "" || strings.Trim(limit, "0123456789") != "" {
		return out, nil
	}
	if n, ok := py.ParseInt(limit); ok && slices.Contains(ContextLimits[:], n) {
		out.ContextLimit = n
	}
	return out, nil
}

func SetDefaults(ctx context.Context, q db.Querier, model, effort string, contextLimit any) error {
	chosen := pricing.DefaultModel
	if model != "" {
		chosen = model
	} else if current, err := Defaults(ctx, q); err == nil {
		chosen = current.Model
	}
	m, err := CleanModel(chosen)
	if err != nil {
		return err
	}
	e, err := CleanModelEffort(m, effort)
	if err != nil {
		return err
	}
	limit, err := CleanModelContextLimit(m, contextLimit)
	if err != nil {
		return err
	}
	if err := setPinSafe(ctx, q, assistant.KeyDefaultModel, m.ID); err != nil {
		return err
	}
	if err := setPinSafe(ctx, q, assistant.KeyDefaultEffort, e); err != nil {
		return err
	}
	return setPinSafe(ctx, q, assistant.KeyDefaultContextLimit, strconv.FormatInt(limit, 10))
}

func setPinSafe(ctx context.Context, q db.Querier, key, value string) error {
	err := settings.Default.Set(ctx, q, key, value, 0)
	var pinned *settings.PinnedError
	if errors.As(err, &pinned) {
		return OptionError(pinned.Error())
	}
	return err
}

func TitleFrom(text string) string {
	for _, line := range py.SplitLines(text) {
		line = py.Strip(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) <= TitleChars {
			return line
		}
		return py.Clip(line, TitleChars-1) + "…"
	}
	return ""
}

type NewThread struct {
	Kind         string
	Title        *string
	ParentID     *int64
	JobID        *int64
	ProjectID    *int64
	Model        string
	Effort       string
	ContextLimit any
}

func Create(ctx context.Context, q db.Querier, now float64, n NewThread) (int64, error) {
	chosen, err := Defaults(ctx, q)
	if err != nil {
		return 0, err
	}
	if n.ProjectID != nil && *n.ProjectID != 0 {
		err := q.QueryRowContext(ctx, "SELECT effort, context_limit FROM projects WHERE id = ?", *n.ProjectID).Scan(&chosen.Effort, &chosen.ContextLimit)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("threads: no project %d", *n.ProjectID)
		}
		if err != nil {
			return 0, err
		}
	}
	model := pricing.DefaultModel
	if n.Kind == Chat {
		model = chosen.Model
	}
	if n.Model != "" {
		model = n.Model
	}
	m, err := CleanModel(model)
	if err != nil {
		return 0, err
	}
	effort := chosen.Effort
	if n.Kind != Chat {
		var ok bool
		if effort, ok, err = EffortOf(ctx, q, n.Kind); err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("threads: no effort for kind %s", py.Repr(n.Kind))
		}
	}
	if n.Effort != "" {
		effort = n.Effort
	}
	if effort, err = CleanModelEffort(m, effort); err != nil {
		return 0, err
	}
	limit := chosen.ContextLimit
	if n.ContextLimit != nil {
		if limit, err = CleanContextLimit(n.ContextLimit); err != nil {
			return 0, err
		}
	}
	if limit, err = CleanModelContextLimit(m, limit); err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO threads (kind, title, job_id, parent_thread_id, model, effort, context_limit, project_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		n.Kind, n.Title, n.JobID, n.ParentID, m.ID, effort, limit, n.ProjectID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func SetOptions(ctx context.Context, q db.Querier, threadID int64, model, effort string, contextLimit any) (bool, error) {
	if model == "" {
		err := q.QueryRowContext(ctx, "SELECT model FROM threads WHERE id = ? AND kind = ? AND deleted_at IS NULL", threadID, Chat).Scan(&model)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	m, err := CleanModel(model)
	if err != nil {
		return false, err
	}
	e, err := CleanEffort(effort)
	if err != nil {
		return false, err
	}
	e = SnapEffort(m, e)
	limit, err := CleanContextLimit(contextLimit)
	if err != nil {
		return false, err
	}
	limit = SnapContextLimit(m, limit)
	return changed(q.ExecContext(ctx, "UPDATE threads SET model = ?, effort = ?, context_limit = ? WHERE id = ? AND kind = ?", m.ID, e, limit, threadID, Chat))
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

type Thread struct {
	ID                   int64      `json:"id"`
	Kind                 string     `json:"kind"`
	Title                *string    `json:"title"`
	JobID                *int64     `json:"job_id"`
	ParentThreadID       *int64     `json:"parent_thread_id"`
	Model                string     `json:"model"`
	Effort               string     `json:"effort"`
	AllowedURLs          []string   `json:"allowed_urls"`
	CreatedAt            float64    `json:"created_at"`
	UpdatedAt            float64    `json:"updated_at"`
	Archived             store.Flag `json:"archived"`
	Pinned               store.Flag `json:"pinned"`
	ContextLimit         *int64     `json:"context_limit"`
	BranchedFromThreadID *int64     `json:"branched_from_thread_id"`
	BranchedFromSeq      *int64     `json:"branched_from_seq"`
	Role                 *string    `json:"role"`
	ParentRunID          *int64     `json:"parent_run_id"`
	ProjectID            *int64     `json:"project_id"`
	DeletedAt            *float64   `json:"deleted_at"`
}

const Columns = "id, kind, title, job_id, parent_thread_id, model, effort, allowed_urls, created_at, updated_at, archived, pinned, " +
	"context_limit, branched_from_thread_id, branched_from_seq, role, parent_run_id, project_id, deleted_at"

func Scan(row interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var urls string
	err := row.Scan(&t.ID, &t.Kind, &t.Title, &t.JobID, &t.ParentThreadID, &t.Model, &t.Effort, &urls, &t.CreatedAt, &t.UpdatedAt,
		&t.Archived, &t.Pinned, &t.ContextLimit, &t.BranchedFromThreadID, &t.BranchedFromSeq, &t.Role, &t.ParentRunID,
		&t.ProjectID, &t.DeletedAt)
	if err != nil {
		return Thread{}, err
	}
	if err := json.Unmarshal([]byte(urls), &t.AllowedURLs); err != nil {
		return Thread{}, fmt.Errorf("threads: thread %d allowed_urls: %w", t.ID, err)
	}
	return t, nil
}

func Get(ctx context.Context, q db.Querier, threadID int64) (*Thread, error) {
	t, err := Scan(q.QueryRowContext(ctx, "SELECT "+Columns+" FROM threads WHERE id = ? AND deleted_at IS NULL", threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func ContextLimitOf(t Thread) int64 {
	if t.ContextLimit == nil || *t.ContextLimit == 0 {
		return DefaultContextLimit
	}
	return *t.ContextLimit
}

func SetTitle(ctx context.Context, q db.Querier, threadID int64, title string) error {
	changed, err := changed(q.ExecContext(ctx, "UPDATE threads SET title = ? WHERE id = ? AND title IS NULL", title, threadID))
	if err != nil || !changed {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", threadID, title)
	return err
}

func RootKind(ctx context.Context, q db.Querier, threadID int64) (string, error) {
	kind := ""
	seen := map[int64]bool{}
	for next := &threadID; next != nil && !seen[*next]; {
		seen[*next] = true
		var parent *int64
		var k string
		err := q.QueryRowContext(ctx, "SELECT kind, parent_thread_id FROM threads WHERE id = ?", *next).Scan(&k, &parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return "", err
		}
		kind, next = k, parent
	}
	return kind, nil
}

func AllowedURLs(ctx context.Context, q db.Querier, threadID int64) (map[string]bool, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT allowed_urls FROM threads WHERE id = ?", threadID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var urls []string
	if err := json.Unmarshal([]byte(raw), &urls); err != nil {
		return nil, fmt.Errorf("threads: thread %d allowed_urls: %w", threadID, err)
	}
	out := make(map[string]bool, len(urls))
	for _, u := range urls {
		out[u] = true
	}
	return out, nil
}

func AddAllowedURLs(ctx context.Context, d *db.DB, threadID int64, urls []string) error {
	var add []string
	for _, u := range urls {
		if u != "" {
			add = append(add, u)
		}
	}
	if len(add) == 0 {
		return nil
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		known, err := AllowedURLs(ctx, tx, threadID)
		if err != nil {
			return err
		}
		merged := maps.Clone(known)
		for _, u := range add {
			merged[u] = true
		}
		if len(merged) == len(known) {
			return nil
		}
		list := make([]string, 0, len(merged))
		for u := range merged {
			list = append(list, u)
		}
		slices.Sort(list)
		_, err = tx.ExecContext(ctx, "UPDATE threads SET allowed_urls = ? WHERE id = ?", py.JSONDumps(list), threadID)
		return err
	})
}

func URLsIn(text string) []string {
	matches, _ := urlRe.FindIter(text)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = strings.TrimRight(m.Text(), ".,;:!?")
	}
	return out
}

type Listed struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Title       *string    `json:"title"`
	Pinned      store.Flag `json:"pinned"`
	Archived    store.Flag `json:"archived"`
	CreatedAt   float64    `json:"created_at"`
	UpdatedAt   float64    `json:"updated_at"`
	ProjectID   *int64     `json:"project_id"`
	ProjectName *string    `json:"project_name"`
	RunState    *string    `json:"run_state"`
	Cost        float64    `json:"cost"`
}

type ListOpts struct {
	Match     string
	Limit     int
	Archived  bool
	ProjectID int64
}

const listSQL = "SELECT t.id, t.kind, t.title, t.pinned, t.archived, t.created_at, t.updated_at, t.project_id, " +
	"(SELECT name FROM projects p WHERE p.id = t.project_id) AS project_name, " +
	"(SELECT state FROM runs r WHERE r.thread_id = t.id ORDER BY r.id DESC LIMIT 1) AS run_state, " +
	"(SELECT COALESCE(SUM(cost_usd), 0) FROM usage u WHERE u.thread_id = t.id) AS cost " +
	"FROM threads t WHERE t.kind = ? AND t.archived = ? AND t.deleted_at IS NULL"

func ListThreads(ctx context.Context, q db.Querier, o ListOpts) ([]Listed, error) {
	query := listSQL
	args := []any{Chat, store.Bit(o.Archived)}
	if o.ProjectID != 0 {
		query += " AND t.project_id = ?"
		args = append(args, o.ProjectID)
	}
	if o.Match != "" {
		query += " AND t.id IN (SELECT thread_id FROM thread_fts WHERE thread_fts MATCH ?)"
		args = append(args, o.Match)
	}
	query += " ORDER BY t.pinned DESC, t.updated_at DESC, t.id DESC LIMIT ?"
	args = append(args, o.Limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Listed, error) {
		var l Listed
		return l, r.Scan(&l.ID, &l.Kind, &l.Title, &l.Pinned, &l.Archived, &l.CreatedAt, &l.UpdatedAt, &l.ProjectID,
			&l.ProjectName, &l.RunState, &l.Cost)
	})
}

func SetFlag(ctx context.Context, q db.Querier, threadID int64, column string, value bool) (bool, error) {
	switch column {
	case "pinned", "archived":
	default:
		return false, fmt.Errorf("threads: %s", py.Repr(column))
	}
	return changed(q.ExecContext(ctx,
		"UPDATE threads SET "+column+" = ? WHERE id = ? AND kind = ? AND deleted_at IS NULL", store.Bit(value), threadID, Chat))
}

func Purge(ctx context.Context, q db.Querier, threadID int64, now float64) error {
	const runs = "SELECT id FROM runs WHERE thread_id = ?"
	for _, stmt := range []string{
		"DELETE FROM run_events WHERE run_id IN (" + runs + ")",
		"DELETE FROM context_ledger WHERE run_id IN (" + runs + ")",
		"DELETE FROM tool_results WHERE run_id IN (" + runs + ")",
		"DELETE FROM tool_calls WHERE run_id IN (" + runs + ")",
		"DELETE FROM context_projections WHERE thread_id = ?",
		"DELETE FROM thread_queue WHERE thread_id = ?",
		"DELETE FROM thread_plans WHERE thread_id = ?",
		"DELETE FROM thread_messages WHERE thread_id = ?",
		"DELETE FROM thread_fts WHERE thread_id = ?",
	} {
		if _, err := q.ExecContext(ctx, stmt, threadID); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx,
		"UPDATE threads SET deleted_at = ?, archived = 1, pinned = 0, title = NULL WHERE id = ?", now, threadID)
	return err
}
