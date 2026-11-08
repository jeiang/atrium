package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type Line struct {
	Level     int     `json:"level"`
	Index     int     `json:"index"`
	From      int64   `json:"from"`
	To        int64   `json:"to"`
	State     string  `json:"state"`
	Bytes     int     `json:"bytes"`
	Text      string  `json:"text"`
	ResultIDs []int64 `json:"result_ids,omitempty"`
}

type View struct {
	Lines  []Line
	Bytes  int
	Over   bool
	Target int
	High   int
}

type storedView struct {
	SummaryCount int
	MessageCount int
	Over         bool
	Lines        []Line
}

func ViewSettings(ctx context.Context, q db.Querier) (low, high, tail int, err error) {
	resolve := func(key string) (int64, error) {
		found, err := settings.From(ctx).Resolve(ctx, q, key)
		if err != nil {
			return 0, err
		}
		return found.Int()
	}
	l, err := resolve(ViewLowKey)
	if err != nil {
		return 0, 0, 0, err
	}
	h, err := resolve(ViewHighKey)
	if err != nil {
		return 0, 0, 0, err
	}
	t, err := resolve(TailMessagesKey)
	if err != nil {
		return 0, 0, 0, err
	}
	return int(l), int(h), int(t), nil
}

// Build returns the thread's stable view. A new batch is computed only when
// the summary or message count changed; otherwise the persisted batch is
// returned unchanged, so a settings change applies to the next batch.
func Build(ctx context.Context, q db.Querier, threadID int64, now float64) (*View, error) {
	low, high, tail, ok, err := bounds(ctx, q, threadID)
	if err != nil || !ok {
		return nil, err
	}
	summaryCount, err := count(ctx, q, "SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND state IN (?, ?)", threadID, Summary, Fallback)
	if err != nil {
		return nil, err
	}
	messageCount, err := count(ctx, q, "SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND superseded = 0", threadID)
	if err != nil {
		return nil, err
	}
	stored, err := loadView(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	if stored != nil && stored.SummaryCount == summaryCount && stored.MessageCount == messageCount {
		return &View{Lines: stored.Lines, Bytes: bytesOf(stored.Lines), Over: stored.Over, Target: low, High: high}, nil
	}
	nodes, err := Tree(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	lines, over := reduce(nodes, low, high, tail)
	v := &View{Lines: lines, Bytes: bytesOf(lines), Over: over, Target: low, High: high}
	if err := saveView(ctx, q, threadID, summaryCount, messageCount, over, lines, now); err != nil {
		return nil, err
	}
	return v, nil
}

func Older(ctx context.Context, q db.Querier, threadID, before int64) ([]Line, error) {
	low, high, tail, ok, err := bounds(ctx, q, threadID)
	if err != nil || !ok {
		return nil, err
	}
	nodes, err := Tree(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	kept := nodes[:0]
	for _, n := range nodes {
		if n.ToSeq < before {
			kept = append(kept, n)
		}
	}
	lines, _ := reduce(kept, low, high, tail)
	return lines, nil
}

func bounds(ctx context.Context, q db.Querier, threadID int64) (low, high, tail int, ok bool, err error) {
	low, high, tail, err = ViewSettings(ctx, q)
	if err != nil {
		return 0, 0, 0, false, err
	}
	thread, err := threads.Get(ctx, q, threadID)
	if err != nil || thread == nil {
		return 0, 0, 0, false, err
	}
	if limit := threads.ContextLimitOf(*thread); limit > 0 && limit*4 < int64(high) {
		high = int(limit * 4)
	}
	return min(low, high), high, tail, true, nil
}

func reduce(nodes []Node, low, high, tail int) ([]Line, bool) {
	byKey := make(map[[2]int]Node, len(nodes))
	var lines []Line
	for _, n := range nodes {
		byKey[[2]int{n.Level, n.Index}] = n
		if n.Level == 0 {
			lines = append(lines, lineOf(n))
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Index < lines[j].Index })
	total := bytesOf(lines)
	if total <= high {
		return lines, total > low
	}
	T := float64(len(lines))
	for total > low {
		best, bestPri := -1, math.Inf(-1)
		for i := 0; i+1 < len(lines)-tail; i++ {
			a, b := lines[i], lines[i+1]
			if a.Level != b.Level || a.Index%2 != 0 || b.Index != a.Index+1 {
				continue
			}
			parent, ok := byKey[[2]int{a.Level + 1, a.Index / 2}]
			if !ok || (parent.State != Summary && parent.State != Direct && parent.State != Fallback) {
				continue
			}
			pri := (T+1)/math.Pow(2, float64(a.Level)) - float64(a.Index)
			if pri > bestPri {
				bestPri, best = pri, i
			}
		}
		if best < 0 {
			break
		}
		a, b := lines[best], lines[best+1]
		parent := byKey[[2]int{a.Level + 1, a.Index / 2}]
		line := lineOf(parent)
		line.From, line.To = a.From, b.To
		lines[best] = line
		lines = append(lines[:best+1], lines[best+2:]...)
		total += line.Bytes - a.Bytes - b.Bytes
	}
	return lines, total > low
}

func lineOf(n Node) Line {
	return Line{Level: n.Level, Index: n.Index, From: n.FromSeq, To: n.ToSeq, State: n.State, Bytes: n.Bytes, Text: n.Text, ResultIDs: n.ResultIDs}
}

func bytesOf(lines []Line) int {
	total := 0
	for _, l := range lines {
		total += l.Bytes
	}
	return total
}

func count(ctx context.Context, q db.Querier, query string, args ...any) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

func loadView(ctx context.Context, q db.Querier, threadID int64) (*storedView, error) {
	var v storedView
	var raw string
	var over int
	err := q.QueryRowContext(ctx,
		"SELECT summary_count, message_count, over_target, lines_json FROM memory_views WHERE thread_id = ?", threadID).
		Scan(&v.SummaryCount, &v.MessageCount, &over, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.Over = over != 0
	if err := json.Unmarshal([]byte(raw), &v.Lines); err != nil {
		return nil, err
	}
	return &v, nil
}

func saveView(ctx context.Context, q db.Querier, threadID int64, summaryCount, messageCount int, over bool, lines []Line, now float64) error {
	raw, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	flag := 0
	if over {
		flag = 1
	}
	_, err = q.ExecContext(ctx,
		"INSERT INTO memory_views (thread_id, summary_count, message_count, over_target, lines_json, created_at) VALUES (?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(thread_id) DO UPDATE SET summary_count = excluded.summary_count, message_count = excluded.message_count, "+
			"over_target = excluded.over_target, lines_json = excluded.lines_json, created_at = excluded.created_at",
		threadID, summaryCount, messageCount, flag, string(raw), now)
	return err
}

// Describe renders one view line for the operator command.
func (l Line) Describe() string {
	text := fmt.Sprintf("level %d idx %d %d..%d %s %d bytes", l.Level, l.Index, l.From, l.To, l.State, l.Bytes)
	if len(l.ResultIDs) > 0 {
		ids := make([]string, len(l.ResultIDs))
		for i, id := range l.ResultIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		text += " result_ids=" + strings.Join(ids, ",")
	}
	return text
}
