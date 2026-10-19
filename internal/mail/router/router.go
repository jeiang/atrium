// Package router is src/atrium/router.py: it decides where each eligible
// inbox message belongs (correction, operator rule, learned rule, nearest
// neighbours, the local model, then the account's fallback) and records the
// decision. It never writes to a mailbox.
package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/py"
)

var Stages = [...]string{"correction", "operator", "learned", "knn", "llm", "fallback"}

// Decision is one routing decision; Dest, Key and Confidence are nil where Python has None.
type Decision struct {
	Stage      string
	Action     string
	Dest       *string
	Key        *string
	Confidence *float64
	Detail     py.Dict
}

// LLMContext is router.LlmContext: the folders the local model may pick for one account in one pass.
type LLMContext struct {
	URL       string
	Folders   []string
	Catalog   string
	Available bool
	Down      bool
}

// Judge is llm.JudgeFolder.
type Judge func(ctx context.Context, url string, folders []string, catalog string, neighbors []llm.Neighbor, m llm.Message, body string) (string, bool, error)

// Querier is the read side of *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Router routes the eligible messages of an account.
type Router struct {
	DB      *db.DB
	ChatURL string
	// Judge defaults to llm.JudgeFolder.
	Judge Judge
	// Now defaults to the wall clock.
	Now func() float64
	// Log defaults to logx.Logger("atrium.router").
	Log *slog.Logger
}

func (r *Router) judge() Judge {
	if r.Judge != nil {
		return r.Judge
	}
	return llm.JudgeFolder
}

func (r *Router) now() float64 {
	if r.Now != nil {
		return r.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (r *Router) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return logx.Logger("atrium.router")
}

// Eligible is router.eligible: the account's undecided, read, unflagged inbox messages by id.
func Eligible(ctx context.Context, q Querier, account string) ([]rules.Message, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+rules.MessageColumns+" FROM messages m WHERE m.account = ? AND m.in_inbox = 1 AND m.seen = 1 "+
			"AND m.flagged = 0 AND m.sent = 0 AND m.gone_at IS NULL "+
			"AND NOT EXISTS (SELECT 1 FROM decisions d WHERE d.message_id = m.id) ORDER BY m.id", account)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (rules.Message, error) { return rules.ScanMessage(r) })
}

// Neighbor is a filed message near the one being routed.
type Neighbor struct {
	ID         int64
	Folder     string
	FromDomain string
	Subject    string
	Similarity float64
}

// NearestFiled is router.nearest_filed: up to count filed neighbours of the message in its account, nearest
// first; none when the message has no embedding.
func NearestFiled(ctx context.Context, q Querier, m *rules.Message, count int) ([]Neighbor, error) {
	var blob []byte
	err := q.QueryRowContext(ctx, "SELECT embedding FROM message_vec WHERE rowid = ?", m.ID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var total int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM message_vec WHERE account = ?", m.Account).Scan(&total); err != nil {
		return nil, err
	}
	k := max(count*4, 32)
	for {
		hits, err := knn(ctx, q, blob, min(k, max(total, 1), embed.KNNMax), m.Account)
		if err != nil {
			return nil, err
		}
		var ids []any
		for _, h := range hits {
			if h.rowid != m.ID {
				ids = append(ids, h.rowid)
			}
		}
		info := map[int64]Neighbor{}
		if len(ids) > 0 {
			rows, err := q.QueryContext(ctx,
				"SELECT id, folder, from_domain, subject FROM filed_messages WHERE id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
			if err != nil {
				return nil, err
			}
			found, err := db.Collect(rows, func(r *sql.Rows) (Neighbor, error) {
				var n Neighbor
				err := r.Scan(&n.ID, &n.Folder, &n.FromDomain, &n.Subject)
				return n, err
			})
			if err != nil {
				return nil, err
			}
			for _, n := range found {
				info[n.ID] = n
			}
		}
		var neighbors []Neighbor
		for _, h := range hits {
			if n, ok := info[h.rowid]; ok {
				n.Similarity = max(1.0-h.distance, 0.0)
				neighbors = append(neighbors, n)
			}
		}
		if len(neighbors) >= count || k >= min(total, embed.KNNMax) {
			return neighbors[:min(count, len(neighbors))], nil
		}
		k *= 4
	}
}

type hit struct {
	rowid    int64
	distance float64
}

func knn(ctx context.Context, q Querier, blob []byte, k int, account string) ([]hit, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT rowid, distance FROM message_vec WHERE embedding MATCH ? AND k = ? AND account = ?", blob, k, account)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (hit, error) {
		var h hit
		err := r.Scan(&h.rowid, &h.distance)
		return h, err
	})
}

// Vote is router.vote: the folder with the most similarity among the neighbours and its share of the total.
func Vote(neighbors []Neighbor) (folder string, share float64, ok bool) {
	var order []string
	scores := map[string]float64{}
	for _, n := range neighbors {
		if _, seen := scores[n.Folder]; !seen {
			order = append(order, n.Folder)
		}
		scores[n.Folder] += n.Similarity
	}
	values := make([]float64, len(order))
	for i, f := range order {
		values[i] = scores[f]
	}
	total := py.Sum(values)
	if len(scores) == 0 || total <= 0 {
		return "", 0, false
	}
	for i, f := range order {
		if i == 0 || scores[f] > scores[folder] || scores[f] == scores[folder] && f > folder {
			folder = f
		}
	}
	return folder, scores[folder] / total, true
}

// NewLLMContext is router.llm_context: the folders with enough filed messages, which the model may pick from.
func NewLLMContext(ctx context.Context, q Querier, account, url string) (LLMContext, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT folder FROM filed_messages WHERE account = ? GROUP BY folder HAVING COUNT(*) >= ? ORDER BY folder",
		account, config.FolderMinMessages)
	if err != nil {
		return LLMContext{}, err
	}
	folders, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var f string
		return f, r.Scan(&f)
	})
	if err != nil {
		return LLMContext{}, err
	}
	if len(folders) == 0 {
		return LLMContext{URL: url, Folders: []string{}}, nil
	}
	args := []any{account}
	for _, f := range folders {
		args = append(args, f)
	}
	rows, err = q.QueryContext(ctx,
		"SELECT folder, from_domain FROM filed_messages WHERE account = ? AND folder IN ("+
			strings.TrimSuffix(strings.Repeat("?,", len(folders)), ",")+")", args...)
	if err != nil {
		return LLMContext{}, err
	}
	senders, err := db.Collect(rows, func(r *sql.Rows) (llm.FolderSender, error) {
		var s llm.FolderSender
		return s, r.Scan(&s.Folder, &s.FromDomain)
	})
	if err != nil {
		return LLMContext{}, err
	}
	return LLMContext{URL: url, Folders: folders, Catalog: llm.FolderCatalog(senders), Available: true}, nil
}

// MessageBody is router.message_body: the indexed body, or "" without an index row.
func MessageBody(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, id int64) (string, error) {
	var body sql.NullString
	err := q.QueryRowContext(ctx, "SELECT body FROM message_fts WHERE rowid = ?", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body.String, err
}

// Decide is router.decide; nil means the message stays undecided this pass.
func (r *Router) Decide(ctx context.Context, q Querier, m *rules.Message, ruleList []rules.Rule, lc *LLMContext) (*Decision, error) {
	spec, ok := config.Account(m.Account)
	if !ok {
		return nil, fmt.Errorf("%w: %s", httpx.ErrKey, py.Repr(m.Account))
	}
	corrected, err := corrections.Get(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	if corrected != nil {
		return &Decision{Stage: "correction", Action: "file", Dest: new(corrected.Folder), Key: new(corrected.Source), Confidence: new(1.0)}, nil
	}
	rule, err := rules.FirstMatch(m, ruleList)
	if err != nil {
		return nil, err
	}
	if rule != nil {
		return &Decision{Stage: "operator", Action: rule.Action, Dest: rule.Dest, Key: new(rule.ID)}, nil
	}
	learned, err := rules.LearnedMatch(ctx, q, m)
	if err != nil {
		return nil, err
	}
	if learned != nil {
		return &Decision{Stage: "learned", Action: "file", Dest: new(learned.Dest), Key: new(learned.Level), Confidence: new(learned.Purity),
			Detail: py.Dict{{Key: "n", Value: learned.N}}}, nil
	}
	if !m.Embedded {
		return nil, nil
	}
	neighbors, err := NearestFiled(ctx, q, m, max(config.KNNK, config.LLMNeighbors))
	if err != nil {
		return nil, err
	}
	if folder, share, ok := Vote(neighbors[:min(config.KNNK, len(neighbors))]); ok && share >= config.KNNMinShare {
		return &Decision{Stage: "knn", Action: "file", Dest: new(folder), Key: new("vote"), Confidence: new(share)}, nil
	}
	if len(neighbors) > 0 && lc.Available {
		if lc.Down {
			return nil, nil
		}
		body, err := MessageBody(ctx, q, m.ID)
		if err != nil {
			return nil, err
		}
		near := make([]llm.Neighbor, min(config.LLMNeighbors, len(neighbors)))
		for i := range near {
			near[i] = llm.Neighbor{Folder: neighbors[i].Folder, FromDomain: neighbors[i].FromDomain, Subject: neighbors[i].Subject}
		}
		folder, confident, err := r.judge()(ctx, lc.URL, lc.Folders, lc.Catalog, near,
			llm.Message{Subject: m.Subject, FromName: m.FromName, FromAddr: m.FromAddr}, body)
		if err != nil {
			return nil, err
		}
		if folder != llm.Uncertain {
			return &Decision{Stage: "llm", Action: "file", Dest: new(folder), Key: new("chat"), Detail: py.Dict{{Key: "confident", Value: confident}}}, nil
		}
	}
	if spec.UncertainDest != "" {
		return &Decision{Stage: "fallback", Action: "file", Dest: new(spec.UncertainDest)}, nil
	}
	return &Decision{Stage: "fallback", Action: "none"}, nil
}

// Record is router.record: it appends the decision to the decisions table.
func (r *Router) Record(ctx context.Context, m *rules.Message, d *Decision) error {
	_, err := r.DB.Write.ExecContext(ctx,
		"INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.Account, r.now(), d.Stage, deref(d.Key), d.Action, deref(d.Dest), derefFloat(d.Confidence), py.JSONDumps(d.Detail))
	return err
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func derefFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// RouteAccount is router.route_account: it decides every eligible message of the account and returns how many
// decisions it recorded. When the model is unavailable the account's model stage is skipped for the rest of the
// pass and the messages that needed it are left for the next one; any other error ends the pass.
func (r *Router) RouteAccount(ctx context.Context, account string) (int, error) {
	pending, err := Eligible(ctx, r.DB.Read, account)
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	ruleList, err := rules.LoadRules(ctx, r.DB.Read, account)
	if err != nil {
		return 0, err
	}
	lc, err := NewLLMContext(ctx, r.DB.Read, account, r.ChatURL)
	if err != nil {
		return 0, err
	}
	decided := 0
	for i := range pending {
		m := &pending[i]
		d, err := r.Decide(ctx, r.DB.Read, m, ruleList, &lc)
		var unavailable *httpx.ServiceUnavailable
		if errors.As(err, &unavailable) {
			r.log().Warn(fmt.Sprintf("chat unavailable, deferring %s: %s", account, unavailable))
			lc.Down = true
			continue
		}
		if err != nil {
			return decided, err
		}
		if d == nil {
			continue
		}
		if err := r.Record(ctx, m, d); err != nil {
			return decided, err
		}
		decided++
	}
	return decided, nil
}
