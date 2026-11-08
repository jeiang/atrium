package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/rules"
)

var log = logx.Logger("atrium.router")

var Stages = [...]string{"correction", "operator", "learned", "knn", "llm", "fallback"}

type Decision struct {
	Stage      string
	Action     string
	Dest       *string
	Key        *string
	Confidence *float64
	Detail     map[string]any
}

type LLMContext struct {
	URL       string
	Folders   []string
	Catalog   string
	Available bool
	Down      bool
}

type Querier = db.Querier

type Tuning struct {
	Learned      rules.Thresholds
	KNNK         int
	KNNMinShare  float64
	KNNMax       int
	LLMNeighbors int
	FolderMin    int
	Uncertain    string
}

func ReadTuning(ctx context.Context, q Querier, account string) (Tuning, error) {
	r := endpoint.Read(ctx, q)
	t := Tuning{
		KNNK: r.Int(mail.KeyKNNK), KNNMinShare: r.Float(mail.KeyKNNMinShare), KNNMax: r.Int(mail.KeyKNNMax),
		LLMNeighbors: r.Int(mail.KeyLLMNeighbors), FolderMin: r.Int(mail.KeyFolderMinMessages),
	}
	if err := r.Err(); err != nil {
		return Tuning{}, err
	}
	var err error
	if t.Learned, err = rules.ReadThresholds(ctx, q); err != nil {
		return Tuning{}, err
	}
	t.Uncertain, err = mail.UncertainDest(ctx, q, account)
	return t, err
}

type Router struct {
	DB  *db.DB
	Now func() float64
}

func (r *Router) now() float64 {
	if r.Now != nil {
		return r.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

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

type Neighbor struct {
	ID         int64
	Folder     string
	FromDomain string
	Subject    string
	Similarity float64
}

func NearestFiled(ctx context.Context, q Querier, m *rules.Message, count, knnMax int) ([]Neighbor, error) {
	idx, err := embed.Active(ctx, q)
	if err != nil {
		return nil, err
	}
	table := idx.MessageTable()
	var blob []byte
	err = q.QueryRowContext(ctx, "SELECT embedding FROM "+table+" WHERE rowid = ?", m.ID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var total int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE account = ?", m.Account).Scan(&total); err != nil {
		return nil, err
	}
	k := max(count*4, 32)
	for {
		hits, err := knn(ctx, q, table, blob, min(k, max(total, 1), knnMax), m.Account)
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
		if len(neighbors) >= count || k >= min(total, knnMax) {
			return neighbors[:min(count, len(neighbors))], nil
		}
		k *= 4
	}
}

type hit struct {
	rowid    int64
	distance float64
}

func knn(ctx context.Context, q Querier, table string, blob []byte, k int, account string) ([]hit, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT rowid, distance FROM "+table+" WHERE embedding MATCH ? AND k = ? AND account = ?", blob, k, account)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (hit, error) {
		var h hit
		err := r.Scan(&h.rowid, &h.distance)
		return h, err
	})
}

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
	var total float64
	for _, v := range values {
		total += v
	}
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

func NewLLMContext(ctx context.Context, q Querier, account, url string, folderMin int) (LLMContext, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT folder FROM filed_messages WHERE account = ? GROUP BY folder HAVING COUNT(*) >= ? ORDER BY folder",
		account, folderMin)
	if err != nil {
		return LLMContext{}, err
	}
	folders, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var f string
		err := r.Scan(&f)
		return f, err
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
		"SELECT folder, from_domain, COUNT(*) FROM filed_messages WHERE account = ? AND folder IN ("+
			strings.TrimSuffix(strings.Repeat("?,", len(folders)), ",")+") AND from_domain != '' "+
			"GROUP BY folder, from_domain", args...)
	if err != nil {
		return LLMContext{}, err
	}
	senders, err := db.Collect(rows, func(r *sql.Rows) (llm.FolderSender, error) {
		var s llm.FolderSender
		err := r.Scan(&s.Folder, &s.FromDomain, &s.N)
		return s, err
	})
	if err != nil {
		return LLMContext{}, err
	}
	return LLMContext{URL: url, Folders: folders, Catalog: llm.FolderCatalog(ctx, senders), Available: true}, nil
}

func (r *Router) Decide(ctx context.Context, q Querier, m *rules.Message, ruleList []rules.Rule, lc *LLMContext, t Tuning) (*Decision, error) {
	_, ok := config.From(ctx).Account(m.Account)
	if !ok {
		return nil, fmt.Errorf("%w: %s", httpx.ErrMissing, strconv.Quote(m.Account))
	}
	corrected, err := corrections.Get(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	if corrected != nil {
		return &Decision{Stage: "correction", Action: string(rules.ActionFile), Dest: new(corrected.Folder), Key: new(corrected.Source), Confidence: new(1.0)}, nil
	}
	rule, err := rules.FirstMatch(m, ruleList, r.now())
	if err != nil {
		return nil, err
	}
	if rule != nil {
		return &Decision{Stage: "operator", Action: string(rule.Action), Dest: rule.Dest, Key: new(rule.ID)}, nil
	}
	learned, err := rules.LearnedMatch(ctx, q, m, t.Learned)
	if err != nil {
		return nil, err
	}
	if learned != nil {
		return &Decision{Stage: "learned", Action: string(rules.ActionFile), Dest: new(learned.Dest), Key: new(learned.Level), Confidence: new(learned.Purity),
			Detail: map[string]any{"n": learned.N}}, nil
	}
	if !m.Embedded {
		return nil, nil
	}
	neighbors, err := NearestFiled(ctx, q, m, max(t.KNNK, t.LLMNeighbors), t.KNNMax)
	if err != nil {
		return nil, err
	}
	if folder, share, ok := Vote(neighbors[:min(t.KNNK, len(neighbors))]); ok && share >= t.KNNMinShare {
		return &Decision{Stage: "knn", Action: string(rules.ActionFile), Dest: new(folder), Key: new("vote"), Confidence: new(share)}, nil
	}
	if len(neighbors) > 0 && lc.Available {
		if lc.Down {
			return nil, nil
		}
		body, err := message.Body(ctx, q, m.ID)
		if err != nil {
			return nil, err
		}
		near := make([]llm.Neighbor, min(t.LLMNeighbors, len(neighbors)))
		for i := range near {
			near[i] = llm.Neighbor{Folder: neighbors[i].Folder, FromDomain: neighbors[i].FromDomain, Subject: neighbors[i].Subject}
		}
		folder, confident, err := llm.JudgeFolder(ctx, lc.URL, lc.Folders, lc.Catalog, near,
			llm.Message{Subject: m.Subject, FromName: m.FromName, FromAddr: m.FromAddr}, body)
		if err != nil {
			return nil, err
		}
		if folder != llm.Uncertain {
			return &Decision{Stage: "llm", Action: string(rules.ActionFile), Dest: new(folder), Key: new("chat"), Detail: map[string]any{"confident": confident}}, nil
		}
	}
	if t.Uncertain != "" {
		return &Decision{Stage: "fallback", Action: string(rules.ActionFile), Dest: new(t.Uncertain)}, nil
	}
	return &Decision{Stage: "fallback", Action: string(rules.ActionNone)}, nil
}

func (r *Router) Record(ctx context.Context, m *rules.Message, d *Decision) error {
	return message.RecordDecision(ctx, r.DB.Write, message.DecisionRecord{
		MessageID: m.ID, Account: m.Account, TS: r.now(), Stage: d.Stage, Key: d.Key, Action: d.Action, Dest: d.Dest,
		Confidence: d.Confidence, Detail: d.Detail,
	})
}

func (r *Router) RouteAccount(ctx context.Context, account string) (int, error) {
	pending, err := Eligible(ctx, r.DB.Read, account)
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	ruleList, err := rules.LoadRules(ctx, r.DB.Read, account)
	if err != nil {
		return 0, err
	}
	ctx, err = embed.WithModels(ctx, r.DB.Read)
	if err != nil {
		return 0, err
	}
	chatURL, err := endpoint.ChatURL(ctx, r.DB.Read)
	if err != nil {
		return 0, err
	}
	tune, err := ReadTuning(ctx, r.DB.Read, account)
	if err != nil {
		return 0, err
	}
	lc, err := NewLLMContext(ctx, r.DB.Read, account, chatURL, tune.FolderMin)
	if err != nil {
		return 0, err
	}
	decided := 0
	for i := range pending {
		m := &pending[i]
		d, err := r.Decide(ctx, r.DB.KNN, m, ruleList, &lc, tune)
		var unavailable *httpx.ServiceUnavailable
		if errors.As(err, &unavailable) {
			log.Warn(fmt.Sprintf("chat unavailable, deferring %s: %s", account, unavailable))
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
