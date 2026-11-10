package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	Direct   = "direct"
	Pending  = "pending"
	Summary  = "summary"
	Fallback = "fallback"
)

type Node struct {
	ID        int64
	ThreadID  int64
	Level     int
	Index     int
	Text      string
	Bytes     int
	State     string
	FromSeq   int64
	ToSeq     int64
	ResultIDs []int64
	CreatedAt float64
}

type nodeKey struct {
	level int
	index int
}

type nodeSpec struct {
	text      string
	bytes     int
	state     string
	fromSeq   int64
	toSeq     int64
	resultIDs []int64
}

func NodeBytes(ctx context.Context, q db.Querier) (int, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, NodeBytesKey)
	if err != nil {
		return 0, err
	}
	n, err := found.Int()
	if err != nil {
		return 0, err
	}
	if n < 1 {
		n = 512
	}
	return int(n), nil
}

func Rebuild(ctx context.Context, q db.Querier, threadID int64, now float64) error {
	limit, err := NodeBytes(ctx, q)
	if err != nil {
		return err
	}
	messages, err := journal.MessagesOf(ctx, q, threadID)
	if err != nil {
		return err
	}
	desired := make(map[nodeKey]nodeSpec, len(messages)*2)
	level := make([]nodeSpec, len(messages))
	for i, m := range messages {
		text, ids, err := render(m)
		if err != nil {
			return err
		}
		spec := fit(text, limit)
		spec.fromSeq, spec.toSeq = m.Seq, m.Seq
		spec.resultIDs = ids
		desired[nodeKey{0, i}] = spec
		level[i] = spec
	}
	for l := 1; len(level) >= 2; l++ {
		next := make([]nodeSpec, 0, len(level)/2)
		for i := 0; i+1 < len(level); i += 2 {
			source := level[i].text + "\n" + level[i+1].text
			spec := fit(source, limit)
			spec.fromSeq, spec.toSeq = level[i].fromSeq, level[i+1].toSeq
			spec.resultIDs = union(level[i].resultIDs, level[i+1].resultIDs)
			desired[nodeKey{l, i / 2}] = spec
			next = append(next, spec)
		}
		level = next
	}
	existing, err := load(ctx, q, threadID)
	if err != nil {
		return err
	}
	seen := make(map[nodeKey]bool, len(existing))
	for _, n := range existing {
		k := nodeKey{n.Level, n.Index}
		seen[k] = true
		spec, ok := desired[k]
		if !ok {
			if _, err := q.ExecContext(ctx, "DELETE FROM memory_jobs WHERE node_id = ?", n.ID); err != nil {
				return err
			}
			if _, err := q.ExecContext(ctx, "DELETE FROM memory_nodes WHERE id = ?", n.ID); err != nil {
				return err
			}
			continue
		}
		delete(desired, k)
		if n.State == Summary || n.State == Fallback {
			if n.FromSeq == spec.fromSeq && n.ToSeq == spec.toSeq {
				continue
			}
		}
		if n.FromSeq == spec.fromSeq && n.ToSeq == spec.toSeq && n.Bytes == spec.bytes && n.Text == spec.text {
			continue
		}
		if _, err := q.ExecContext(ctx,
			"UPDATE memory_nodes SET text = ?, bytes = ?, state = ?, src_from_seq = ?, src_to_seq = ?, result_ids_json = ? WHERE id = ?",
			spec.text, spec.bytes, spec.state, spec.fromSeq, spec.toSeq, resultIDs(spec.resultIDs), n.ID); err != nil {
			return err
		}
	}
	for k, spec := range desired {
		if seen[k] {
			continue
		}
		if _, err := q.ExecContext(ctx,
			"INSERT INTO memory_nodes (thread_id, level, idx, text, bytes, state, src_from_seq, src_to_seq, result_ids_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			threadID, k.level, k.index, spec.text, spec.bytes, spec.state, spec.fromSeq, spec.toSeq, resultIDs(spec.resultIDs), now); err != nil {
			return err
		}
	}
	return Queue(ctx, q, threadID, now)
}

func Tree(ctx context.Context, q db.Querier, threadID int64) ([]Node, error) {
	return load(ctx, q, threadID)
}

func load(ctx context.Context, q db.Querier, threadID int64) ([]Node, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, thread_id, level, idx, text, bytes, state, src_from_seq, src_to_seq, result_ids_json, created_at FROM memory_nodes WHERE thread_id = ? ORDER BY level, idx", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Node, error) {
		var n Node
		var raw *string
		if err := r.Scan(&n.ID, &n.ThreadID, &n.Level, &n.Index, &n.Text, &n.Bytes, &n.State, &n.FromSeq, &n.ToSeq, &raw, &n.CreatedAt); err != nil {
			return Node{}, err
		}
		if raw != nil && *raw != "" {
			if err := json.Unmarshal([]byte(*raw), &n.ResultIDs); err != nil {
				return Node{}, err
			}
		}
		return n, nil
	})
}

func render(m journal.Message) (string, []int64, error) {
	blocks, err := block.DecodeAll(m.Content)
	if err != nil {
		return "", nil, err
	}
	var parts []string
	var ids []int64
	for _, b := range blocks {
		switch b.Kind {
		case block.Text:
			if text := strings.TrimSpace(b.Text()); text != "" {
				parts = append(parts, text)
			}
		case block.ToolUse:
			parts = append(parts, "[tool_use] "+b.String("name")+" "+digest(b.Get("input")))
		case block.ToolResult:
			content := b.String("content")
			if id := resultID(content); id > 0 {
				ids = append(ids, id)
				parts = append(parts, fmt.Sprintf("[tool_result] result_id %d", id))
			} else if content != "" {
				parts = append(parts, "[tool_result] "+clip(content, 200))
			}
		case block.Attachment:
			parts = append(parts, "[attachment]")
		case references.Type:
			parts = append(parts, referenceLine(b.Value))
		}
	}
	return strings.Join(parts, "\n"), ids, nil
}

func digest(value any) string {
	if value == nil {
		return ""
	}
	text := jsonx.Dumps(value, jsonx.Opts{Compact: true, NoEnsureASCII: true})
	return clip(text, 200)
}

func resultID(content string) int64 {
	var found struct {
		ResultID *int64 `json:"result_id"`
	}
	if err := json.Unmarshal([]byte(content), &found); err != nil || found.ResultID == nil {
		return 0
	}
	return *found.ResultID
}

func referenceLine(value map[string]any) string {
	threadID, _ := value["thread_id"].(int64)
	if seq, ok := value["seq"].(int64); ok {
		return fmt.Sprintf("[reference thread %d m%d]", threadID, seq)
	}
	return fmt.Sprintf("[reference thread %d]", threadID)
}

func fit(source string, limit int) nodeSpec {
	spec := nodeSpec{text: source, bytes: len(source), state: Direct}
	if len(source) > limit {
		spec.text, spec.state = clip(source, limit), Pending
	}
	return spec
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	if limit <= 0 {
		return ""
	}
	cut := limit
	for cut > 0 && (text[cut]&0xC0) == 0x80 {
		cut--
	}
	return strings.TrimRight(text[:cut], " \t\n") + "…"
}

func union(a, b []int64) []int64 {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]int64, 0, len(a)+len(b))
	out = append(out, a...)
	for _, id := range b {
		if !contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func contains(ids []int64, id int64) bool {
	for _, found := range ids {
		if found == id {
			return true
		}
	}
	return false
}

func resultIDs(ids []int64) *string {
	if len(ids) == 0 {
		return nil
	}
	text := "["
	for i, id := range ids {
		if i > 0 {
			text += ","
		}
		text += strconv.FormatInt(id, 10)
	}
	text += "]"
	return &text
}
