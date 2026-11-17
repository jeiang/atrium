package application

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/mail/rules"
	"github.com/jeiang/atrium/internal/apps/mail/triage"
	"github.com/jeiang/atrium/internal/db"
)

type RuleDecisionsInput struct {
	ID     string
	Offset int
	Limit  int
}

type RuleDecisionsResult struct {
	Rule       triage.RuleRef `json:"rule"`
	Total      int            `json:"total"`
	Rows       []triage.Row   `json:"rows"`
	Offset     int            `json:"offset"`
	NextOffset int            `json:"next_offset"`
}

func (s *Service) RuleDecisions(ctx context.Context, in RuleDecisionsInput) (application.Read[RuleDecisionsResult], error) {
	data := RuleDecisionsResult{Offset: in.Offset}
	rev, err := s.snapshot(ctx, RulesName, in, func(q db.Querier) error {
		index, err := loadRuleIndex(ctx, q)
		if err != nil {
			return err
		}
		found, err := triage.RuleDecisions(ctx, q, in.ID, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		ref := index.ref(in.ID, 0)
		if ref.State == triage.RuleRemoved && found.Total == 0 {
			return &application.Error{Code: application.NotFound, Message: "unknown rule"}
		}
		data.Rule, data.Rows, data.Total = *ref, found.Rows, found.Total
		if shown := in.Offset + len(found.Rows); shown < found.Total {
			data.NextOffset = shown
		}
		index.link(data.Rows)
		return nil
	})
	return application.Read[RuleDecisionsResult]{Revision: rev, Data: data}, err
}

type ruleIndex struct {
	titles  map[string]string
	changed map[string]float64
}

func loadRuleIndex(ctx context.Context, q db.Querier) (ruleIndex, error) {
	index := ruleIndex{titles: map[string]string{}}
	rows, err := q.QueryContext(ctx, "SELECT id, action, dest FROM rules")
	if err != nil {
		return index, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, action string
		var dest sql.NullString
		if err := rows.Scan(&id, &action, &dest); err != nil {
			return index, err
		}
		index.titles[id] = ruleTitle(rules.Action(action), dest)
	}
	if err := rows.Err(); err != nil {
		return index, err
	}
	index.changed, err = state.LastSucceeded(ctx, q, string(RuleCreateName), string(RuleUpdateName), string(RulesImportName))
	return index, err
}

func ruleTitle(action rules.Action, dest sql.NullString) string {
	switch action {
	case rules.ActionFile:
		if dest.Valid {
			return "File into " + dest.String
		}
		return "File into a folder"
	case rules.ActionDelete:
		return "Delete"
	default:
		return "Leave in place"
	}
}

func (x ruleIndex) ref(id string, decidedAt float64) *triage.RuleRef {
	title, ok := x.titles[id]
	if !ok {
		return &triage.RuleRef{ID: id, State: triage.RuleRemoved}
	}
	ref := &triage.RuleRef{ID: id, Title: &title, State: triage.RuleCurrent}
	if decidedAt > 0 && (x.changed[id] > decidedAt || x.changed[""] > decidedAt) {
		ref.State = triage.RuleChanged
	}
	return ref
}

func (x ruleIndex) link(rows []triage.Row) {
	for i := range rows {
		if rows[i].Stage == triage.RuleStage && rows[i].Key != nil {
			rows[i].Rule = x.ref(*rows[i].Key, rows[i].TS)
		}
	}
}

func linkRules(ctx context.Context, q db.Querier, groups ...[]triage.Row) error {
	needed := false
	for _, rows := range groups {
		for _, r := range rows {
			needed = needed || (r.Stage == triage.RuleStage && r.Key != nil)
		}
	}
	if !needed {
		return nil
	}
	index, err := loadRuleIndex(ctx, q)
	if err != nil {
		return err
	}
	for _, rows := range groups {
		index.link(rows)
	}
	return nil
}
