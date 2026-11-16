package subagents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/textx"
)

const (
	Queued      = "queued"
	Running     = "running"
	Done        = "done"
	ErrorStatus = "error"
	Interrupted = "interrupted"
)

type Gate struct {
	mu     sync.Mutex
	active int
	wake   chan struct{}
}

func NewGate() *Gate { return &Gate{wake: make(chan struct{})} }

func (g *Gate) Acquire(ctx context.Context, limit int) error {
	for {
		g.mu.Lock()
		if g.active < limit {
			g.active++
			g.mu.Unlock()
			return nil
		}
		wake := g.wake
		g.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (g *Gate) Release() {
	g.mu.Lock()
	g.active--
	close(g.wake)
	g.wake = make(chan struct{})
	g.mu.Unlock()
}

var Slots = NewGate()

type Role struct {
	Name        string
	Description string
	Prompt      string
	Effort      string
	Tools       []string
}

var Roles = []Role{
	{
		Name:        "calendar",
		Description: "Calendar events.",
		Prompt:      "Look up events with calendar_list. Return an event list or summary with event ids.",
		Effort:      pricing.Medium,
		Tools:       []string{"calendar_list", "recall"},
	},
	{
		Name:        "mail",
		Description: "The operator's mail and contacts.",
		Prompt:      "Research the operator's mail with mail_search, mail_read and contacts_lookup. Return message ids, dates, senders and short excerpts.",
		Effort:      pricing.Medium,
		Tools:       []string{"contacts_lookup", "mail_read", "mail_search", "recall"},
	},
	{
		Name:        "observability",
		Description: "Grafana metrics, logs, alerts and dashboards.",
		Prompt:      "Investigate with the read-only grafana_* tools. Return a summary of query results with dashboard uids or datasource uids.",
		Effort:      pricing.Medium,
		Tools:       []string{"grafana_alerts", "grafana_datasources", "grafana_query", "grafana_search_dashboards", "recall"},
	},
}

func Lookup(name string) (Role, bool) {
	for _, r := range Roles {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

func Names() []string {
	names := make([]string, len(Roles))
	for i, r := range Roles {
		names[i] = r.Name
	}
	return names
}

func (r Role) System() string {
	return prompts.Subagent + "\n\nAgent `" + r.Name + "`: " + r.Prompt
}

func CreateChild(ctx context.Context, q db.Querier, parentThread, parentRun int64, r Role, title string, project *int64, marks threads.Marks, now float64) (int64, error) {
	return threads.Create(ctx, q, now, threads.Spec{
		Kind: threads.Subagent, Title: &title, ParentID: &parentThread,
		Effort: r.Effort, Role: &r.Name, ParentRunID: &parentRun,
		ProjectID: project, Marks: marks,
	})
}

func DigestOf(ctx context.Context, q db.Querier, threadID int64) (string, error) {
	history, err := journal.LoadHistory(ctx, q, threadID)
	if err != nil {
		return "", err
	}
	messages := history.Messages
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != journal.Assistant {
			continue
		}
		texts, err := block.Texts(messages[i].Content)
		if err != nil {
			return "", fmt.Errorf("subagents: thread %d message %d: %w", threadID, messages[i].ID, err)
		}
		if text := strings.TrimSpace(strings.Join(texts, "\n")); text != "" {
			return textx.Clip(text, assistant.TuningAgentSubagentDigestChars), nil
		}
	}
	return "", nil
}

type Card struct {
	ThreadID    int64    `json:"thread_id"`
	Kind        string   `json:"kind"`
	Role        *string  `json:"role"`
	Title       *string  `json:"title"`
	Effort      string   `json:"effort"`
	RunID       *int64   `json:"run_id"`
	ParentRunID *int64   `json:"parent_run_id"`
	Status      string   `json:"status"`
	Current     *string  `json:"current"`
	Elapsed     *float64 `json:"elapsed"`
	Error       *string  `json:"error"`
	Steps       int64    `json:"steps"`
	Input       int64    `json:"input"`
	Output      int64    `json:"output"`
	CacheRead   int64    `json:"cache_read"`
	CacheWrite  int64    `json:"cache_write"`
	Cost        float64  `json:"cost"`
}

var statusOfRun = map[string]string{
	journal.Running: Running, journal.Done: Done, journal.Failed: ErrorStatus, journal.Interrupted: Interrupted,
}

func runCard(ctx context.Context, q db.Querier, t threads.Thread, run *journal.Run, now float64) (Card, error) {
	elapsed := max(now-t.CreatedAt, 0)
	card := Card{
		ThreadID: t.ID, Kind: string(t.Kind), Role: t.Role, Title: t.Title, Effort: t.Effort, ParentRunID: t.ParentRunID,
		Status: Queued, Elapsed: &elapsed,
	}
	if run == nil {
		return card, nil
	}
	status, ok := statusOfRun[run.State]
	if !ok {
		return Card{}, fmt.Errorf("subagents: run %d has unknown state %s", run.ID, strconv.Quote(run.State))
	}
	card.Effort, card.RunID, card.ParentRunID = run.Effort, &run.ID, run.ParentRunID
	card.Status, card.Error, card.Steps = status, run.Error, run.Steps
	switch {
	case run.EndedAt != nil:
		card.Elapsed = new(*run.EndedAt - run.StartedAt)
	case run.State == journal.Running:
		card.Elapsed = new(max(now-run.StartedAt, 0))
	default:
		card.Elapsed = nil
	}
	if run.State == journal.Running {
		var current string
		err := q.QueryRowContext(ctx, "SELECT name FROM tool_calls WHERE run_id = ? AND state = ? ORDER BY id DESC LIMIT 1", run.ID, journal.Started).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Card{}, err
		}
		if err == nil {
			card.Current = &current
		}
	}
	totals, err := inference.TotalsOfRun(ctx, q, run.ID)
	if err != nil {
		return card, err
	}
	card.Input, card.Output, card.CacheRead, card.CacheWrite, card.Cost = totals.Input, totals.Output, totals.CacheRead, totals.CacheWrite, totals.Cost
	return card, nil
}

func ChildCard(ctx context.Context, q db.Querier, t threads.Thread, now float64) (Card, error) {
	runs, err := journal.RunsOf(ctx, q, t.ID)
	if err != nil {
		return Card{}, err
	}
	var run *journal.Run
	if len(runs) > 0 {
		run = &runs[len(runs)-1]
	}
	return runCard(ctx, q, t, run, now)
}
