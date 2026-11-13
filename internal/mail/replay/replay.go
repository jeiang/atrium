package replay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/classify"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/mail/rules"
)

const StageNone = "none"

type Options struct {
	Account string
	Limit   int
	Model   bool
}

type Line struct {
	ID           int64    `json:"id"`
	Account      string   `json:"account"`
	Stage        string   `json:"stage"`
	Action       string   `json:"action"`
	Dest         *string  `json:"dest"`
	Key          *string  `json:"key"`
	Confidence   *float64 `json:"confidence"`
	StoredStage  string   `json:"stored_stage"`
	StoredAction string   `json:"stored_action"`
	StoredDest   *string  `json:"stored_dest"`
	Vector       bool     `json:"vector"`
}

type Summary struct {
	Messages int
	Stages   map[string]int
	Differ   int
}

type Reason struct {
	Text  string
	Count int
}

type Result struct {
	Lines   []Line
	Summary Summary
	Reasons []Reason
}

type stored struct {
	stage  string
	action string
	dest   sql.NullString
}

func Run(ctx context.Context, q db.Querier, opts Options, judge *classify.Service) (Result, error) {
	if opts.Model && judge == nil {
		return Result{}, errors.New("replay: model mode needs a classifier")
	}
	now, ok, err := latestDecisionTime(ctx, q)
	if err != nil || !ok {
		return Result{}, err
	}
	idx, err := embed.Active(ctx, q)
	if err != nil {
		return Result{}, err
	}
	messages, err := decidedMessages(ctx, q, opts)
	if err != nil {
		return Result{}, err
	}
	if opts.Model {
		if ctx, err = embed.WithModels(ctx, q); err != nil {
			return Result{}, err
		}
	}
	r := &router.Router{Now: func() float64 { return now }, Classifier: judge}
	paid := &classify.Deferral{}
	ruleLists := map[string][]rules.Rule{}
	tunings := map[string]router.Tuning{}
	contexts := map[string]*router.LLMContext{}
	res := Result{Summary: Summary{Stages: map[string]int{}}}
	reasons := map[string]int{}
	for i := range messages {
		m := &messages[i]
		ruleList, ok := ruleLists[m.Account]
		if !ok {
			if ruleList, err = rules.LoadRules(ctx, q, m.Account); err != nil {
				return Result{}, err
			}
			ruleLists[m.Account] = ruleList
		}
		tuning, ok := tunings[m.Account]
		if !ok {
			if tuning, err = router.ReadTuning(ctx, q, m.Account); err != nil {
				return Result{}, err
			}
			tunings[m.Account] = tuning
		}
		lc, ok := contexts[m.Account]
		if !ok {
			built, err := router.NewLLMContext(ctx, q, m.Account, tuning.FolderMin, paid)
			if err != nil {
				return Result{}, err
			}
			built.NoModel = !opts.Model
			lc = &built
			contexts[m.Account] = lc
		}
		vector, err := embed.MessageVectorExists(ctx, q, idx, m.ID)
		if err != nil {
			return Result{}, err
		}
		m.Embedded = vector
		previous, err := latestDecision(ctx, q, m.ID)
		if err != nil {
			return Result{}, err
		}
		line := Line{
			ID: m.ID, Account: m.Account, StoredStage: previous.stage, StoredAction: previous.action,
			StoredDest: nullable(previous.dest), Vector: vector,
		}
		decision, err := r.Decide(ctx, q, m, ruleList, lc, tuning)
		switch {
		case errors.Is(err, httpx.ErrMissing):
			line.Stage, line.Action = StageNone, StageNone
			reasons[fmt.Sprintf("account %q is not configured in the database", m.Account)]++
		case err != nil:
			return Result{}, err
		case decision == nil:
			line.Stage, line.Action = StageNone, StageNone
		default:
			line.Stage, line.Action = decision.Stage, decision.Action
			line.Dest, line.Key = decision.Dest, decision.Key
			line.Confidence = rounded(decision.Confidence)
		}
		if differs(line) {
			res.Summary.Differ++
		}
		res.Summary.Messages++
		res.Summary.Stages[line.Stage]++
		res.Lines = append(res.Lines, line)
	}
	for text, count := range reasons {
		res.Reasons = append(res.Reasons, Reason{Text: text, Count: count})
	}
	sort.Slice(res.Reasons, func(i, j int) bool { return res.Reasons[i].Text < res.Reasons[j].Text })
	return res, nil
}

func differs(line Line) bool {
	if line.Stage != line.StoredStage || line.Action != line.StoredAction {
		return true
	}
	if line.Dest == nil || line.StoredDest == nil {
		return line.Dest != nil || line.StoredDest != nil
	}
	return *line.Dest != *line.StoredDest
}

func nullable(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return new(value.String)
}

func rounded(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return new(math.Round(*value*1e4) / 1e4)
}

func latestDecisionTime(ctx context.Context, q db.Querier) (float64, bool, error) {
	var ts sql.NullFloat64
	if err := q.QueryRowContext(ctx, "SELECT MAX(ts) FROM decisions").Scan(&ts); err != nil {
		return 0, false, err
	}
	return ts.Float64, ts.Valid, nil
}

func decidedMessages(ctx context.Context, q db.Querier, opts Options) ([]rules.Message, error) {
	query := "SELECT " + rules.MessageColumns + " FROM messages m WHERE EXISTS (SELECT 1 FROM decisions d WHERE d.message_id = m.id)"
	var args []any
	if opts.Account != "" {
		query += " AND m.account = ?"
		args = append(args, opts.Account)
	}
	query += " ORDER BY m.id"
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (rules.Message, error) { return rules.ScanMessage(r) })
}

func latestDecision(ctx context.Context, q db.Querier, id int64) (stored, error) {
	var s stored
	err := q.QueryRowContext(ctx, "SELECT stage, action, dest FROM decisions WHERE message_id = ? ORDER BY id DESC LIMIT 1", id).
		Scan(&s.stage, &s.action, &s.dest)
	if errors.Is(err, sql.ErrNoRows) {
		return stored{}, errors.New("replay: a decided message has no decision")
	}
	return s, err
}
