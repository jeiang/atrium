package context

import (
	gocontext "context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type Message struct {
	Role    string
	Content []json.RawMessage
	Seq     *int64
	RunID   *int64
}

func (m Message) Wire() json.RawMessage {
	var b strings.Builder
	b.WriteString(`{"role":`)
	b.WriteString(py.JSONDumps(m.Role, compactJSON))
	b.WriteString(`,"content":[`)
	for i, raw := range m.Content {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(raw)
	}
	b.WriteString("]}")
	return json.RawMessage(b.String())
}

type headMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type Projection struct {
	ID             int64
	Kind           string
	BaseSeq        int64
	StripFromSeq   int64
	StripBeforeSeq int64
	Head           []Message
}

func GetProjection(ctx gocontext.Context, q db.Querier, threadID int64) (Projection, error) {
	var p Projection
	var head string
	err := q.QueryRowContext(ctx,
		"SELECT id, kind, base_seq, strip_from_seq, strip_before_seq, head_json FROM context_projections WHERE thread_id = ? ORDER BY id DESC LIMIT 1",
		threadID).Scan(&p.ID, &p.Kind, &p.BaseSeq, &p.StripFromSeq, &p.StripBeforeSeq, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return Projection{Head: []Message{}}, nil
	}
	if err != nil {
		return Projection{}, err
	}
	var rows []headMessage
	if err := json.Unmarshal([]byte(head), &rows); err != nil {
		return Projection{}, fmt.Errorf("context: projection %d head_json: %w", p.ID, err)
	}
	p.Head = make([]Message, len(rows))
	for i, m := range rows {
		p.Head[i] = Message{Role: m.Role, Content: m.Content}
	}
	return p, nil
}

func dumpHead(head []Message) (string, error) {
	items := make([]any, len(head))
	for i, m := range head {
		content, err := decodeAll(m.Content)
		if err != nil {
			return "", err
		}
		items[i] = py.Dict{{Key: "role", Value: m.Role}, {Key: "content", Value: content}}
	}
	return py.JSONDumps(items, compactJSON), nil
}

func SaveProjection(ctx gocontext.Context, q db.Querier, threadID, runID int64, kind string, baseSeq, stripFromSeq, stripBeforeSeq int64, head []Message, savedTokens int64, now float64) (int64, error) {
	body, err := dumpHead(head)
	if err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO context_projections (thread_id, run_id, ts, kind, base_seq, strip_from_seq, strip_before_seq, head_json, saved_tokens) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		threadID, runID, now, kind, baseSeq, stripFromSeq, stripBeforeSeq, body, savedTokens)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type LedgerRow struct {
	ID            int64
	RunID         int64
	ItemID        string
	Kind          string
	ResultID      *int64
	SourceIDsJSON *string
	Synopsis      *string
	Tokens        int64
	State         string
	StagedAt      *float64
	CommittedAt   *float64
	FromSeq       *int64
	ToSeq         *int64
}

func (r LedgerRow) from() int64 { return deref(r.FromSeq) }
func (r LedgerRow) to() int64   { return deref(r.ToSeq) }

func (r LedgerRow) sources() ([]any, error) {
	if r.SourceIDsJSON == nil || *r.SourceIDsJSON == "" {
		return nil, nil
	}
	v, err := py.JSONLoads([]byte(*r.SourceIDsJSON))
	if err != nil {
		return nil, fmt.Errorf("context: ledger %d source_ids_json: %w", r.ID, err)
	}
	list, _ := v.([]any)
	return list, nil
}

type Ledger struct {
	Rows  []LedgerRow
	index map[string]int
}

func (l Ledger) Get(itemID string) (LedgerRow, bool) {
	i, ok := l.index[itemID]
	if !ok {
		return LedgerRow{}, false
	}
	return l.Rows[i], true
}

func LedgerRows(ctx gocontext.Context, q db.Querier, threadID int64) (Ledger, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT l.id, l.run_id, l.item_id, l.kind, l.result_id, l.source_ids_json, l.synopsis, l.tokens, l.state, l.staged_at, "+
			"l.committed_at, l.from_seq, l.to_seq FROM context_ledger l JOIN runs r ON r.id = l.run_id WHERE r.thread_id = ? ORDER BY l.id",
		threadID)
	if err != nil {
		return Ledger{}, err
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (LedgerRow, error) {
		var l LedgerRow
		err := r.Scan(&l.ID, &l.RunID, &l.ItemID, &l.Kind, &l.ResultID, &l.SourceIDsJSON, &l.Synopsis, &l.Tokens, &l.State,
			&l.StagedAt, &l.CommittedAt, &l.FromSeq, &l.ToSeq)
		return l, err
	})
	if err != nil {
		return Ledger{}, err
	}
	out := Ledger{index: make(map[string]int, len(list))}
	for _, r := range list {
		if i, ok := out.index[r.ItemID]; ok {
			out.Rows[i] = r
			continue
		}
		out.index[r.ItemID] = len(out.Rows)
		out.Rows = append(out.Rows, r)
	}
	return out, nil
}

func (l Ledger) withState(states ...string) []LedgerRow {
	var out []LedgerRow
	for _, r := range l.Rows {
		for _, s := range states {
			if r.State == s {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

type block struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
}

func headOf(raw json.RawMessage) block {
	var b block
	_ = json.Unmarshal(raw, &b)
	return b
}

func isThinking(kind string) bool { return kind == Thinking || kind == RedactedThinking }

func hasType(content []json.RawMessage, kind string) bool {
	for _, raw := range content {
		if headOf(raw).Type == kind {
			return true
		}
	}
	return false
}

func TurnStart(messages []journal.Message) int64 {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == journal.User && journal.HasText(messages[i].Content) {
			return messages[i].Seq
		}
	}
	return 0
}

func StripThinking(content []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(content))
	for _, raw := range content {
		if !isThinking(headOf(raw).Type) {
			out = append(out, raw)
		}
	}
	return out
}

func dropStubValue(resultID *int64) string {
	var id any
	if resultID != nil {
		id = *resultID
	}
	return py.JSONDumps(py.Dict{{Key: "result_id", Value: id}, {Key: "dropped", Value: true}})
}

func dropStub(raw json.RawMessage, resultID *int64) (json.RawMessage, error) {
	return SetField(raw, "content", dropStubValue(resultID))
}

func SetField(raw json.RawMessage, key string, value any) (json.RawMessage, error) {
	d, err := blockDict(raw)
	if err != nil {
		return nil, err
	}
	return encode(d.Set(key, value)), nil
}

func foldBlock(r LedgerRow) (json.RawMessage, error) {
	sources, err := r.sources()
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("[context fold of seq %s-%s] %s", optInt(r.FromSeq), optInt(r.ToSeq), optStr(r.Synopsis))
	if len(sources) > 0 {
		text += "\nSources: " + joinStr(sources)
	}
	return TextBlock(text), nil
}

func TextBlock(text string) json.RawMessage {
	return encode(py.Dict{{Key: "type", Value: Text}, {Key: "text", Value: text}})
}

func mergeRoles(messages []Message) []Message {
	var out []Message
	for _, m := range messages {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			latest := m
			if m.Seq == nil {
				latest = out[n-1]
			}
			content := append(append([]json.RawMessage{}, out[n-1].Content...), m.Content...)
			out[n-1] = Message{Role: m.Role, Content: content, Seq: latest.Seq, RunID: latest.RunID}
			continue
		}
		out = append(out, m)
	}
	return out
}

func tailOf(ctx gocontext.Context, q db.Querier, threadID int64, p Projection) ([]journal.Message, error) {
	all, err := journal.MessagesOf(ctx, q, threadID, false)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, m := range all {
		if m.Seq >= p.BaseSeq {
			out = append(out, m)
		}
	}
	return out, nil
}

func inFold(folds []LedgerRow, seq int64) bool {
	for _, f := range folds {
		if f.from() < seq && seq <= f.to() {
			return true
		}
	}
	return false
}

func ActiveMessages(ctx gocontext.Context, q db.Querier, threadID int64, systemSig string) ([]Message, Projection, error) {
	p, err := GetProjection(ctx, q, threadID)
	if err != nil {
		return nil, Projection{}, err
	}
	ledger, err := LedgerRows(ctx, q, threadID)
	if err != nil {
		return nil, Projection{}, err
	}
	dropped := map[string]LedgerRow{}
	for _, r := range ledger.withState(Dropped) {
		dropped[r.ItemID] = r
	}
	folds := ledger.withState(Folded)
	foldAt := map[int64]LedgerRow{}
	for _, f := range folds {
		foldAt[f.from()] = f
	}
	out := make([]Message, 0, len(p.Head))
	for _, m := range p.Head {
		out = append(out, Message{Role: m.Role, Content: append([]json.RawMessage{}, m.Content...)})
	}
	tail, err := tailOf(ctx, q, threadID, p)
	if err != nil {
		return nil, Projection{}, err
	}
	for _, m := range tail {
		seq := m.Seq
		if f, ok := foldAt[seq]; ok {
			b, err := foldBlock(f)
			if err != nil {
				return nil, Projection{}, err
			}
			out = append(out, Message{Role: journal.User, Content: []json.RawMessage{b}, Seq: &seq, RunID: m.RunID})
			continue
		}
		if inFold(folds, seq) {
			continue
		}
		content := m.Content
		if m.Role == journal.User {
			content = make([]json.RawMessage, len(m.Content))
			for i, raw := range m.Content {
				content[i] = raw
				h := headOf(raw)
				if h.Type != ToolResult {
					continue
				}
				if r, ok := dropped[h.ToolUseID]; ok {
					if content[i], err = dropStub(raw, r.ResultID); err != nil {
						return nil, Projection{}, err
					}
				}
			}
		} else if (p.StripFromSeq <= seq && seq < p.StripBeforeSeq) || (systemSig != "" && (m.SystemSig == nil || *m.SystemSig != systemSig)) {
			content = StripThinking(content)
			if len(content) == 0 {
				continue
			}
		}
		out = append(out, Message{Role: m.Role, Content: content, Seq: &seq, RunID: m.RunID})
	}
	return mergeRoles(out), p, nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func optInt(p *int64) string {
	if p == nil {
		return "None"
	}
	return fmt.Sprint(*p)
}

func optStr(p *string) string {
	if p == nil {
		return "None"
	}
	return *p
}

func joinStr(values []any) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = str(v)
	}
	return strings.Join(parts, ", ")
}
