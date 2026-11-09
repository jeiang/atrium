package sources

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type questions struct{}

func Questions() attention.Source { return questions{} }

func (questions) Name() string { return "questions" }

func (questions) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT r.thread_id, COALESCE(t.title, ''), c.args_json, c.started_at FROM tool_calls c "+
			"JOIN runs r ON r.id = c.run_id LEFT JOIN threads t ON t.id = r.thread_id "+
			"WHERE r.state = 'parked' AND c.name = 'ask_operator' AND c.state = 'started' ORDER BY c.id DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []attention.Item
	seen := map[int64]bool{}
	for rows.Next() {
		var threadID int64
		var title, argsJSON string
		var started float64
		if err := rows.Scan(&threadID, &title, &argsJSON, &started); err != nil {
			return nil, err
		}
		if seen[threadID] {
			continue
		}
		seen[threadID] = true
		var args struct {
			Question string `json:"question"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		detail := "Question"
		if title != "" {
			detail += " in " + title
		}
		items = append(items, attention.Item{
			Kind: "question", SourceID: strconv.FormatInt(threadID, 10), Title: args.Question, Detail: detail,
			Created: started, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "answer", Label: "Answer"}},
		})
	}
	return items, rows.Err()
}
