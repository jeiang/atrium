package context

import (
	gocontext "context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
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
	b.WriteString(jsonx.Dumps(m.Role, compactJSON))
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

type projection struct {
	ID             int64
	BaseSeq        int64
	StripFromSeq   int64
	StripBeforeSeq int64
	Head           []Message
}

func getProjection(ctx gocontext.Context, q db.Querier, threadID int64) (projection, error) {
	var p projection
	var head string
	err := q.QueryRowContext(ctx,
		"SELECT id, base_seq, strip_from_seq, strip_before_seq, head_json FROM context_projections WHERE thread_id = ? ORDER BY id DESC LIMIT 1",
		threadID).Scan(&p.ID, &p.BaseSeq, &p.StripFromSeq, &p.StripBeforeSeq, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return projection{Head: []Message{}}, nil
	}
	if err != nil {
		return projection{}, err
	}
	var rows []headMessage
	if err := json.Unmarshal([]byte(head), &rows); err != nil {
		return projection{}, fmt.Errorf("context: projection %d head_json: %w", p.ID, err)
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
		items[i] = map[string]any{"role": m.Role, "content": content}
	}
	return jsonx.Dumps(items, compactJSON), nil
}

func saveProjection(ctx gocontext.Context, q db.Querier, threadID, runID int64, baseSeq, stripFromSeq, stripBeforeSeq int64, head []Message, savedTokens int64, now float64) (int64, error) {
	body, err := dumpHead(head)
	if err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO context_projections (thread_id, run_id, ts, base_seq, strip_from_seq, strip_before_seq, head_json, saved_tokens) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		threadID, runID, now, baseSeq, stripFromSeq, stripBeforeSeq, body, savedTokens)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type State uint8

const (
	StateActive State = iota
	StatePinned
	StateStagedDrop
	StateDropped
	StateStagedFold
	StateFolded
)

func parseState(value string) (State, bool) {
	switch value {
	case Active:
		return StateActive, true
	case Pinned:
		return StatePinned, true
	case StagedDrop:
		return StateStagedDrop, true
	case Dropped:
		return StateDropped, true
	case StagedFold:
		return StateStagedFold, true
	case Folded:
		return StateFolded, true
	}
	return StateActive, false
}

type FoldRef struct {
	From     int64
	To       int64
	Synopsis string
	Sources  []any
}

type Entry struct {
	ID       string
	RunID    int64
	Tokens   int64
	State    State
	ResultID *int64
	Fold     *FoldRef
}

type Ledger struct {
	Entries []Entry
	index   map[string]int
}

func (l Ledger) Get(itemID string) (Entry, bool) {
	i, ok := l.index[itemID]
	if !ok {
		return Entry{}, false
	}
	return l.Entries[i], true
}

func (l Ledger) withState(states ...State) []Entry {
	var out []Entry
	for _, e := range l.Entries {
		if slices.Contains(states, e.State) {
			out = append(out, e)
		}
	}
	return out
}

type ledgerRow struct {
	ID            int64
	RunID         int64
	ItemID        string
	Kind          string
	ResultID      *int64
	SourceIDsJSON *string
	Synopsis      *string
	Tokens        int64
	State         string
	FromSeq       *int64
	ToSeq         *int64
}

func (r ledgerRow) entry() (Entry, bool, error) {
	state, ok := parseState(r.State)
	if !ok {
		return Entry{}, false, nil
	}
	e := Entry{ID: r.ItemID, RunID: r.RunID, Tokens: r.Tokens, State: state}
	if r.Kind != ItemFold {
		e.ResultID = r.ResultID
		return e, true, nil
	}
	if r.FromSeq == nil || r.ToSeq == nil {
		return Entry{}, false, nil
	}
	sources, err := decodeSources(r.SourceIDsJSON)
	if err != nil {
		return Entry{}, false, err
	}
	e.Fold = &FoldRef{From: *r.FromSeq, To: *r.ToSeq, Synopsis: derefStr(r.Synopsis), Sources: sources}
	return e, true, nil
}

func decodeSources(raw *string) ([]any, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	v, err := jsonx.Loads([]byte(*raw))
	if err != nil {
		return nil, fmt.Errorf("context: ledger source_ids_json: %w", err)
	}
	list, _ := v.([]any)
	return list, nil
}

func loadLedger(ctx gocontext.Context, q db.Querier, threadID int64) (Ledger, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT l.id, l.run_id, l.item_id, l.kind, l.result_id, l.source_ids_json, l.synopsis, l.tokens, l.state, l.from_seq, l.to_seq "+
			"FROM context_ledger l JOIN runs r ON r.id = l.run_id WHERE r.thread_id = ? ORDER BY l.id", threadID)
	if err != nil {
		return Ledger{}, err
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (ledgerRow, error) {
		var l ledgerRow
		err := r.Scan(&l.ID, &l.RunID, &l.ItemID, &l.Kind, &l.ResultID, &l.SourceIDsJSON, &l.Synopsis, &l.Tokens, &l.State, &l.FromSeq, &l.ToSeq)
		return l, err
	})
	if err != nil {
		return Ledger{}, err
	}
	out := Ledger{index: make(map[string]int, len(list))}
	for _, row := range list {
		e, ok, err := row.entry()
		if err != nil {
			return Ledger{}, err
		}
		if !ok {
			continue
		}
		if i, ok := out.index[e.ID]; ok {
			out.Entries[i] = e
			continue
		}
		out.index[e.ID] = len(out.Entries)
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

func isThinking(kind string) bool { return kind == block.Thinking || kind == block.RedactedThinking }

func hasType(content []json.RawMessage, kind string) (bool, error) {
	return block.HasKind(content, kind)
}

func StripThinking(content []json.RawMessage) ([]json.RawMessage, error) {
	blocks, err := block.DecodeAll(content)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(content))
	for _, b := range blocks {
		if !isThinking(b.Kind) {
			out = append(out, b.Raw)
		}
	}
	return out, nil
}

func dropStubValue(resultID *int64) string {
	var id any
	if resultID != nil {
		id = *resultID
	}
	return jsonx.Dumps(map[string]any{"result_id": id, "dropped": true})
}

func dropStub(raw json.RawMessage, resultID *int64) (json.RawMessage, error) {
	return SetField(raw, "content", dropStubValue(resultID))
}

func SetField(raw json.RawMessage, key string, value any) (json.RawMessage, error) {
	d, err := blockDict(raw)
	if err != nil {
		return nil, err
	}
	d[key] = value
	return encode(d), nil
}

func TextBlock(text string) json.RawMessage {
	return encode(map[string]any{"type": Text, "text": text})
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

func tailOf(ctx gocontext.Context, q db.Querier, threadID int64, p projection) ([]journal.Message, error) {
	history, err := journal.LoadHistory(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	out := history.Messages[:0:0]
	for _, m := range history.Messages {
		if m.Seq >= p.BaseSeq {
			out = append(out, m)
		}
	}
	return out, nil
}

func inFold(folds []Entry, seq int64) bool {
	for _, f := range folds {
		if f.Fold.From < seq && seq <= f.Fold.To {
			return true
		}
	}
	return false
}

func blockDict(raw json.RawMessage) (map[string]any, error) {
	v, err := jsonx.Loads(raw)
	if err != nil {
		return nil, fmt.Errorf("context: content block: %w", err)
	}
	d, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("context: content block is %s, not an object", fmt.Sprintf("%T", v))
	}
	return d, nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
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
