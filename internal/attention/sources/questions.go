package sources

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type questions struct{}

func Questions() attention.Source { return questions{} }

func (questions) Name() string { return "questions" }

func (questions) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	found, err := journal.ParkedQuestions(ctx, q)
	if err != nil {
		return nil, err
	}
	var items []attention.Item
	seen := map[int64]bool{}
	for _, question := range found {
		if seen[question.ThreadID] {
			continue
		}
		seen[question.ThreadID] = true
		var args struct {
			Question string `json:"question"`
		}
		_ = json.Unmarshal([]byte(question.ArgsJSON), &args)
		detail := "Question"
		if question.Title != "" {
			detail += " in " + question.Title
		}
		items = append(items, attention.Item{
			Kind: "question", SourceID: strconv.FormatInt(question.ThreadID, 10), Title: args.Question, Detail: detail,
			Created: question.StartedAt, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "answer", Label: "Answer"}},
		})
	}
	return items, nil
}
