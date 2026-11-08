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
	"time"
	"unicode/utf8"

	"regexp"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/textx"
)

type Kind string

const (
	Chat     Kind = "chat"
	Cron     Kind = "cron"
	Nightly  Kind = "nightly"
	Subagent Kind = "subagent"
)

func ParseKind(value string) (Kind, bool) {
	switch Kind(value) {
	case Chat, Cron, Nightly, Subagent:
		return Kind(value), true
	}
	return "", false
}

var effortKeys = map[Kind]string{
	Cron:     assistant.KeyEffortCron,
	Nightly:  assistant.KeyEffortNightly,
	Subagent: assistant.KeyEffortSubagent,
}

func EffortOf(ctx context.Context, q db.Querier, kind Kind) (string, bool, error) {
	key, ok := effortKeys[kind]
	if !ok {
		return "", false, nil
	}
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	if err != nil {
		return "", true, err
	}
	effort, err := CleanEffort(found.Value)
	return effort, true, err
}

const (
	TitleChars = 80
)

var urlRe = regexp.MustCompile(`https?://[^\s<>\"'()\[\]]+`)

type OptionError string

func (e OptionError) Error() string { return string(e) }

func CleanEffort(value string) (string, error) {
	if !slices.Contains(pricing.Efforts, value) {
		return "", OptionError("effort must be one of " + strings.Join(pricing.Efforts, ", "))
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
	if model.SupportsEffort(pricing.Medium) {
		return pricing.Medium
	}
	return model.Efforts[0]
}

func SnapContextLimit(model pricing.Model, limit int64) int64 {
	if limit <= model.ContextWindow {
		return limit
	}
	best := int64(0)
	for _, n := range assistant.ContextLimits {
		if n <= model.ContextWindow && n > best {
			best = n
		}
	}
	if best == 0 {
		return assistant.DefaultContextLimit
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
		limit, ok = numx.ParseInt(string(v))
	case string:
		limit, ok = numx.ParseInt(v)
	}
	if ok && slices.Contains(assistant.ContextLimits, limit) {
		return limit, nil
	}
	sizes := make([]string, len(assistant.ContextLimits))
	for i, n := range assistant.ContextLimits {
		sizes[i] = fmt.Sprintf("%dK", n/1000)
	}
	return 0, OptionError("context limit must be one of " + strings.Join(sizes, ", "))
}

type Options struct {
	Model        string   `json:"model"`
	Effort       string   `json:"effort"`
	ContextLimit int64    `json:"context_limit"`
	Thinking     string   `json:"thinking"`
	WebSearch    bool     `json:"web_search"`
	CostCapUSD   *float64 `json:"cost_cap_usd"`
}

func (o Options) Validate() (Options, error) {
	m, err := CleanModel(o.Model)
	if err != nil {
		return Options{}, err
	}
	if o.Effort, err = CleanModelEffort(m, o.Effort); err != nil {
		return Options{}, err
	}
	if o.ContextLimit, err = CleanModelContextLimit(m, o.ContextLimit); err != nil {
		return Options{}, err
	}
	if o.Thinking == "" {
		o.Thinking = m.DefaultThinking()
	}
	if !m.ValidThinking(o.Thinking, o.Effort) {
		return Options{}, OptionError("thinking " + strconv.Quote(o.Thinking) + " is not available at " + o.Effort + " effort on " + m.ID)
	}
	return o, nil
}

func Defaults(ctx context.Context, q db.Querier) (Options, error) {
	out := Options{Model: pricing.DefaultModel, Effort: pricing.Medium, ContextLimit: assistant.DefaultContextLimit, WebSearch: true}
	if m, err := CleanModel(out.Model); err == nil {
		out.Thinking = m.DefaultThinking()
	}
	found, err := settings.From(ctx).Resolve(ctx, q, assistant.KeyDefaultEffort)
	if err != nil {
		return Options{}, err
	}
	if slices.Contains(pricing.Efforts, found.Value) {
		out.Effort = found.Value
	}
	found, err = settings.From(ctx).Resolve(ctx, q, assistant.KeyDefaultModel)
	if err != nil {
		return out, err
	}
	if _, err := CleanModel(found.Value); err == nil {
		out.Model = found.Value
	}
	found, err = settings.From(ctx).Resolve(ctx, q, assistant.KeyDefaultContextLimit)
	if err != nil {
		return out, err
	}
	if n, ok := numx.ParseInt(found.Value); ok && slices.Contains(assistant.ContextLimits, n) {
		out.ContextLimit = n
	}
	return out, nil
}

func SetDefaults(ctx context.Context, d *db.DB, model, effort string, contextLimit any) error {
	chosen := model
	if chosen == "" {
		current, err := Defaults(ctx, d.Read)
		if err != nil {
			return err
		}
		chosen = current.Model
	}
	limit, err := CleanContextLimit(contextLimit)
	if err != nil {
		return err
	}
	o, err := (Options{Model: chosen, Effort: effort, ContextLimit: limit}).Validate()
	if err != nil {
		return err
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := setPinSafe(ctx, tx, assistant.KeyDefaultModel, o.Model); err != nil {
			return err
		}
		if err := setPinSafe(ctx, tx, assistant.KeyDefaultEffort, o.Effort); err != nil {
			return err
		}
		return setPinSafe(ctx, tx, assistant.KeyDefaultContextLimit, strconv.FormatInt(o.ContextLimit, 10))
	})
}

func setPinSafe(ctx context.Context, q db.Querier, key, value string) error {
	err := settings.From(ctx).Set(ctx, q, key, value)
	var pinned *settings.PinnedError
	if errors.As(err, &pinned) {
		return OptionError(pinned.Error())
	}
	return err
}

func TitleFrom(text string) string {
	for _, line := range textx.SplitLines(text) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) <= TitleChars {
			return line
		}
		return textx.Clip(line, TitleChars-1) + "…"
	}
	return ""
}

type Spec struct {
	Kind                 Kind
	Title                *string
	ParentID             *int64
	JobID                *int64
	ProjectID            *int64
	Model                string
	Effort               string
	ContextLimit         any
	Thinking             string
	WebSearch            *bool
	CostCapUSD           *float64
	Environment          string
	Repo                 string
	BaseBranch           string
	Agent                string
	BetaSet              string
	Speed                string
	AllowedURLs          []string
	Role                 *string
	ParentRunID          *int64
	BranchedFromThreadID *int64
	BranchedFromSeq      *int64
	TaintedAt            *float64
}

func resolve(ctx context.Context, q db.Querier, kind Kind, projectID *int64, explicit Options) (Options, error) {
	chosen, err := Defaults(ctx, q)
	if err != nil {
		return Options{}, err
	}
	projectEffort, projectLimit := false, false
	if projectID != nil && *projectID != 0 {
		var archived int
		err := q.QueryRowContext(ctx, "SELECT effort, context_limit, archived FROM projects WHERE id = ?", *projectID).
			Scan(&chosen.Effort, &chosen.ContextLimit, &archived)
		if errors.Is(err, sql.ErrNoRows) {
			return Options{}, fmt.Errorf("threads: no project %d", *projectID)
		}
		if err != nil {
			return Options{}, err
		}
		if archived != 0 {
			return Options{}, OptionError("project is archived")
		}
		projectEffort, projectLimit = true, true
	}
	if kind != Chat {
		effort, ok, err := EffortOf(ctx, q, kind)
		if err != nil {
			return Options{}, err
		}
		if !ok {
			return Options{}, fmt.Errorf("threads: no effort for kind %s", strconv.Quote(string(kind)))
		}
		chosen.Effort, chosen.Model = effort, pricing.DefaultModel
		projectEffort = false
	}
	if explicit.Model != "" {
		chosen.Model = explicit.Model
	}
	if explicit.Effort != "" {
		chosen.Effort, projectEffort = explicit.Effort, false
	}
	if explicit.ContextLimit != 0 {
		chosen.ContextLimit, projectLimit = explicit.ContextLimit, false
	}
	if explicit.Thinking != "" {
		chosen.Thinking = explicit.Thinking
	}
	if explicit.CostCapUSD != nil {
		chosen.CostCapUSD = explicit.CostCapUSD
	}
	m, err := CleanModel(chosen.Model)
	if err != nil {
		return Options{}, err
	}
	if projectEffort {
		chosen.Effort = SnapEffort(m, chosen.Effort)
	}
	if projectLimit {
		chosen.ContextLimit = SnapContextLimit(m, chosen.ContextLimit)
	}
	return chosen.Validate()
}

func Create(ctx context.Context, q db.Querier, now float64, s Spec) (int64, error) {
	if _, ok := ParseKind(string(s.Kind)); !ok {
		return 0, fmt.Errorf("unknown thread kind %q", s.Kind)
	}
	explicit := Options{Model: s.Model, Effort: s.Effort, Thinking: s.Thinking, CostCapUSD: s.CostCapUSD}
	if s.WebSearch != nil {
		explicit.WebSearch = *s.WebSearch
	}
	if s.ContextLimit != nil {
		limit, err := CleanContextLimit(s.ContextLimit)
		if err != nil {
			return 0, err
		}
		explicit.ContextLimit = limit
	}
	o, err := resolve(ctx, q, s.Kind, s.ProjectID, explicit)
	if err != nil {
		return 0, err
	}
	urls := s.AllowedURLs
	if urls == nil {
		urls = []string{}
	}
	web := true
	if s.WebSearch != nil {
		web = *s.WebSearch
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO threads (kind, title, job_id, parent_thread_id, model, effort, context_limit, project_id, allowed_urls, role, parent_run_id, branched_from_thread_id, branched_from_seq, created_at, updated_at, thinking, web_search, cost_cap_usd, environment, repo, base_branch, agent, beta_set, speed, tainted_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.Kind, s.Title, s.JobID, s.ParentID, o.Model, o.Effort, o.ContextLimit, s.ProjectID, jsonx.Dumps(urls), s.Role, s.ParentRunID,
		s.BranchedFromThreadID, s.BranchedFromSeq, now, now, o.Thinking, b2i(web), o.CostCapUSD, s.Environment, s.Repo, s.BaseBranch, s.Agent, s.BetaSet, s.Speed, s.TaintedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := journal.ReindexThread(ctx, q, id); err != nil {
		return 0, err
	}
	return id, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func Taint(ctx context.Context, q db.Querier, threadID int64, now float64) error {
	_, err := q.ExecContext(ctx, "UPDATE threads SET tainted_at = ? WHERE id = ? AND tainted_at IS NULL", now, threadID)
	return err
}

func TaintedAt(ctx context.Context, q db.Querier, threadID int64) (*float64, error) {
	var at *float64
	err := q.QueryRowContext(ctx, "SELECT tainted_at FROM threads WHERE id = ?", threadID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return at, err
}

func EffortFixed(thinking string) bool { return thinking == "between_tools" || thinking == "disabled" }

type Switch struct {
	Changed          bool
	ThinkingAdjusted bool
	ThinkingDropped  bool
	Warm             bool
	CostUSD          float64
}

func SwitchModel(ctx context.Context, q db.Querier, threadID int64, model, effort string, contextLimit any, now float64) (Switch, error) {
	var current struct {
		model, effort, thinking string
		limit                   *int64
	}
	err := q.QueryRowContext(ctx, "SELECT model, effort, thinking, context_limit FROM threads WHERE id = ? AND kind = ? AND deleted_at IS NULL", threadID, Chat).
		Scan(&current.model, &current.effort, &current.thinking, &current.limit)
	if errors.Is(err, sql.ErrNoRows) {
		return Switch{}, nil
	}
	if err != nil {
		return Switch{}, err
	}
	if model == "" {
		model = current.model
	}
	if effort == "" {
		effort = current.effort
	}
	m, err := CleanModel(model)
	if err != nil {
		return Switch{}, err
	}
	if effort, err = CleanModelEffort(m, effort); err != nil {
		return Switch{}, err
	}
	if effort != current.effort && EffortFixed(current.thinking) {
		return Switch{}, OptionError("effort is fixed on " + current.thinking + " threads")
	}
	if contextLimit == nil || contextLimit == "" {
		if current.limit != nil {
			contextLimit = *current.limit
		} else {
			contextLimit = assistant.DefaultContextLimit
		}
	}
	limit, err := CleanModelContextLimit(m, contextLimit)
	if err != nil {
		return Switch{}, err
	}
	thinking := current.thinking
	adjusted := false
	if !m.ValidThinking(thinking, effort) {
		thinking = m.DefaultThinking()
		adjusted = true
		if !m.ValidThinking(thinking, effort) {
			return Switch{}, OptionError("thinking " + strconv.Quote(thinking) + " is not available at " + effort + " effort on " + m.ID)
		}
	}
	sw := Switch{
		Changed:          model != current.model || effort != current.effort || thinking != current.thinking || current.limit == nil || limit != *current.limit,
		ThinkingAdjusted: adjusted,
	}
	if pricing.ModelRank(model) < pricing.ModelRank(current.model) {
		sw.ThinkingDropped = true
		if err := stripThinking(ctx, q, threadID); err != nil {
			return sw, err
		}
	}
	var ts float64
	var prompt int64
	err = q.QueryRowContext(ctx, "SELECT ts, input_tokens + cache_write_5m + cache_write_1h + cache_read FROM usage WHERE thread_id = ? ORDER BY ts DESC LIMIT 1", threadID).Scan(&ts, &prompt)
	switch {
	case err == nil && now-ts <= 300:
		sw.Warm = true
		sw.CostUSD, err = pricing.CostUSD(model, pricing.Tokens{Input: prompt}, 0)
		if err != nil {
			return sw, err
		}
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return sw, err
	}
	if !sw.Changed {
		return sw, nil
	}
	_, err = q.ExecContext(ctx, "UPDATE threads SET model = ?, effort = ?, context_limit = ?, thinking = ? WHERE id = ? AND kind = ?", model, effort, limit, thinking, threadID, Chat)
	return sw, err
}

func SetOptions(ctx context.Context, q db.Querier, threadID int64, model, effort string, contextLimit any) (bool, error) {
	sw, err := SwitchModel(ctx, q, threadID, model, effort, contextLimit, float64(time.Now().UnixNano())/1e9)
	return sw.Changed, err
}

func stripThinking(ctx context.Context, q db.Querier, threadID int64) error {
	rows, err := q.QueryContext(ctx, "SELECT id, content_json FROM thread_messages WHERE thread_id = ?", threadID)
	if err != nil {
		return err
	}
	type message struct {
		id      int64
		content string
	}
	messages, err := db.Collect(rows, func(r *sql.Rows) (message, error) {
		var m message
		err := r.Scan(&m.id, &m.content)
		return m, err
	})
	if err != nil {
		return err
	}
	for _, m := range messages {
		var blocks []map[string]any
		if err := json.Unmarshal([]byte(m.content), &blocks); err != nil {
			continue
		}
		kept := blocks[:0]
		for _, block := range blocks {
			kind, _ := block["type"].(string)
			if kind == "thinking" || kind == "redacted_thinking" {
				continue
			}
			kept = append(kept, block)
		}
		if len(kept) == len(blocks) {
			continue
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "UPDATE thread_messages SET content_json = ? WHERE id = ?", string(encoded), m.id); err != nil {
			return err
		}
	}
	return nil
}

type Thread struct {
	ID                   int64      `json:"id"`
	Kind                 Kind       `json:"kind"`
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
	Thinking             string     `json:"thinking"`
	WebSearch            store.Flag `json:"web_search"`
	CostCapUSD           *float64   `json:"cost_cap_usd"`
	Environment          *string    `json:"environment"`
	Repo                 *string    `json:"repo"`
	BaseBranch           *string    `json:"base_branch"`
	Agent                *string    `json:"agent"`
	BetaSet              *string    `json:"beta_set"`
	Speed                *string    `json:"speed"`
}

const Columns = "id, kind, title, job_id, parent_thread_id, model, effort, allowed_urls, created_at, updated_at, archived, pinned, " +
	"context_limit, branched_from_thread_id, branched_from_seq, role, parent_run_id, project_id, deleted_at, " +
	"thinking, web_search, cost_cap_usd, environment, repo, base_branch, agent, beta_set, speed"

func Scan(row interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var urls string
	err := row.Scan(&t.ID, &t.Kind, &t.Title, &t.JobID, &t.ParentThreadID, &t.Model, &t.Effort, &urls, &t.CreatedAt, &t.UpdatedAt,
		&t.Archived, &t.Pinned, &t.ContextLimit, &t.BranchedFromThreadID, &t.BranchedFromSeq, &t.Role, &t.ParentRunID,
		&t.ProjectID, &t.DeletedAt, &t.Thinking, &t.WebSearch, &t.CostCapUSD, &t.Environment, &t.Repo,
		&t.BaseBranch, &t.Agent, &t.BetaSet, &t.Speed)
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
		return assistant.DefaultContextLimit
	}
	return *t.ContextLimit
}

func SetTitle(ctx context.Context, q db.Querier, threadID int64, title string) error {
	changed, err := db.Changed(q.ExecContext(ctx, "UPDATE threads SET title = ? WHERE id = ? AND title IS NULL", title, threadID))
	if err != nil || !changed {
		return err
	}
	return journal.ReindexThread(ctx, q, threadID)
}

func RootKind(ctx context.Context, q db.Querier, threadID int64) (Kind, error) {
	kind := Kind("")
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
		kind, next = Kind(k), parent
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
		_, err = tx.ExecContext(ctx, "UPDATE threads SET allowed_urls = ? WHERE id = ?", jsonx.Dumps(list), threadID)
		return err
	})
}

func URLsIn(text string) []string {
	found := urlRe.FindAllString(text, -1)
	out := make([]string, len(found))
	for i, m := range found {
		out[i] = strings.TrimRight(m, ".,;:!?")
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
	After     *ListKey
}

type ListKey struct {
	Pinned    bool
	UpdatedAt float64
	ID        int64
}

func (l Listed) Key() ListKey {
	return ListKey{Pinned: bool(l.Pinned), UpdatedAt: l.UpdatedAt, ID: l.ID}
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
	if k := o.After; k != nil {
		pinned := store.Bit(k.Pinned)
		query += " AND (t.pinned < ? OR (t.pinned = ? AND (t.updated_at < ? OR (t.updated_at = ? AND t.id < ?))))"
		args = append(args, pinned, pinned, k.UpdatedAt, k.UpdatedAt, k.ID)
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

func SetPinned(ctx context.Context, q db.Querier, threadID int64, value bool) (bool, error) {
	return db.Changed(q.ExecContext(ctx,
		"UPDATE threads SET pinned = ? WHERE id = ? AND kind = ? AND deleted_at IS NULL", store.Bit(value), threadID, Chat))
}

func SetArchived(ctx context.Context, q db.Querier, threadID int64, value bool) (bool, error) {
	return db.Changed(q.ExecContext(ctx,
		"UPDATE threads SET archived = ? WHERE id = ? AND kind = ? AND deleted_at IS NULL", store.Bit(value), threadID, Chat))
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
		"DELETE FROM thread_files WHERE thread_id = ?",
	} {
		if _, err := q.ExecContext(ctx, stmt, threadID); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx,
		"UPDATE threads SET deleted_at = ?, archived = 1, pinned = 0, title = NULL WHERE id = ?", now, threadID)
	return err
}
