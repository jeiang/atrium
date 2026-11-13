package application

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	"github.com/jeiang/atrium/internal/mail/rules"
)

const (
	RulePreviewName application.Name = "mail.rule-preview"
	RulesExportName application.Name = "mail.rules-export"
	RuleCreateName  application.Name = "mail.rule-create"
	RuleUpdateName  application.Name = "mail.rule-update"
	RuleReorderName application.Name = "mail.rule-reorder"
	RuleDeleteName  application.Name = "mail.rule-delete"
	RulesImportName application.Name = "mail.rules-import"
)

type RulePreviewInput struct {
	Rule map[string]any `json:"rule"`
}
type RuleInput struct {
	ID   string         `json:"id"`
	Rule map[string]any `json:"rule"`
}
type RuleReorderInput struct {
	ID        string `json:"id"`
	Account   string `json:"account"`
	Direction string `json:"direction"`
}
type RuleReorderResult struct {
	Moved bool `json:"moved"`
}
type RuleDeleteInput struct {
	ID string `json:"id"`
}
type RuleDeleteResult struct {
	Deleted bool `json:"deleted"`
}
type RulesImportInput struct {
	Document any `json:"document"`
}
type RulesImportResult struct {
	Count int `json:"count"`
}

func DescribeRulePreview() application.Definition {
	return application.Describe[RulePreviewInput, ruleedit.Preview](RulePreviewName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeRulesExport() application.Definition {
	return application.Describe[struct{}, rules.ExportedRules](RulesExportName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeRuleCreate() application.Definition {
	return application.Describe[RuleInput, rules.Rule](RuleCreateName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeRuleUpdate() application.Definition {
	return application.Describe[RuleInput, rules.Rule](RuleUpdateName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeRuleReorder() application.Definition {
	return application.Describe[RuleReorderInput, RuleReorderResult](RuleReorderName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeRuleDelete() application.Definition {
	return application.Describe[RuleDeleteInput, RuleDeleteResult](RuleDeleteName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeRulesImport() application.Definition {
	return application.Describe[RulesImportInput, RulesImportResult](RulesImportName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func ruleError(err error) error {
	var invalid rules.RuleError
	if errors.As(err, &invalid) {
		return &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err}
	}
	return err
}

func (s *Service) RulePreview(ctx context.Context, in RulePreviewInput) (application.Read[ruleedit.Preview], error) {
	var out ruleedit.Preview
	rev, err := s.snapshot(ctx, RulePreviewName, in, func(q db.Querier) error {
		var err error
		out, err = ruleedit.PreviewRule(ctx, q, in.Rule, ruleedit.DefaultLimit, s.now())
		return err
	})
	if err != nil {
		return application.Read[ruleedit.Preview]{}, ruleError(err)
	}
	return application.Read[ruleedit.Preview]{Revision: rev, Data: out}, nil
}
func (s *Service) RulesExport(ctx context.Context, in struct{}) (application.Read[rules.ExportedRules], error) {
	var out rules.ExportedRules
	rev, err := s.snapshot(ctx, RulesExportName, in, func(q db.Querier) error {
		var err error
		out, err = rules.ExportRules(ctx, q)
		return err
	})
	if err != nil {
		return application.Read[rules.ExportedRules]{}, err
	}
	return application.Read[rules.ExportedRules]{Revision: rev, Data: out}, nil
}

func ruleCommand[T any](ctx context.Context, s *Service, name application.Name, target string, fn func(*sql.Tx) (T, string, state.Outcome, error)) (application.Change[T], error) {
	var out application.Change[T]
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		data, id, outcome, err := fn(tx)
		if err != nil {
			return err
		}
		out.Data = data
		if id != "" {
			target = id
		}
		out.AuditID, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: "{}"})
		return err
	})
	if err != nil {
		return application.Change[T]{}, s.auditRefusal(ctx, name, target, ruleError(err))
	}
	return out, nil
}

func (s *Service) RuleCreate(ctx context.Context, in RuleInput) (application.Change[rules.Rule], error) {
	return ruleCommand(ctx, s, RuleCreateName, in.ID, func(tx *sql.Tx) (rules.Rule, string, state.Outcome, error) {
		rule, err := ruleedit.CreateRuleTx(ctx, tx, in.Rule)
		return rule, rule.ID, state.OK, err
	})
}
func (s *Service) RuleUpdate(ctx context.Context, in RuleInput) (application.Change[rules.Rule], error) {
	return ruleCommand(ctx, s, RuleUpdateName, in.ID, func(tx *sql.Tx) (rules.Rule, string, state.Outcome, error) {
		rule, err := ruleedit.UpdateRuleTx(ctx, tx, in.ID, in.Rule)
		return rule, "", state.OK, err
	})
}
func (s *Service) RuleReorder(ctx context.Context, in RuleReorderInput) (application.Change[RuleReorderResult], error) {
	return ruleCommand(ctx, s, RuleReorderName, in.ID, func(tx *sql.Tx) (RuleReorderResult, string, state.Outcome, error) {
		var scope *string
		if in.Account != "" {
			scope = &in.Account
		}
		listed, err := ruleedit.ListRules(ctx, tx, scope)
		if err != nil {
			return RuleReorderResult{}, "", "", err
		}
		ordered := make([]string, len(listed))
		for i, r := range listed {
			ordered[i] = r.ID
		}
		index := slices.Index(ordered, in.ID)
		if index < 0 {
			return RuleReorderResult{}, "", "", &application.Error{Code: application.NotFound, Message: "unknown rule"}
		}
		target := index + 1
		if in.Direction == "up" {
			target = index - 1
		}
		if target < 0 || target >= len(ordered) {
			return RuleReorderResult{}, "", state.Noop, nil
		}
		ordered[index], ordered[target] = ordered[target], ordered[index]
		return RuleReorderResult{Moved: true}, "", state.OK, ruleedit.ReorderRulesTx(ctx, tx, ordered)
	})
}
func (s *Service) RuleDelete(ctx context.Context, in RuleDeleteInput) (application.Change[RuleDeleteResult], error) {
	return ruleCommand(ctx, s, RuleDeleteName, in.ID, func(tx *sql.Tx) (RuleDeleteResult, string, state.Outcome, error) {
		deleted, err := ruleedit.DeleteRuleTx(ctx, tx, in.ID)
		outcome := state.OK
		if !deleted {
			outcome = state.Noop
		}
		return RuleDeleteResult{Deleted: deleted}, "", outcome, err
	})
}
func (s *Service) RulesImport(ctx context.Context, in RulesImportInput) (application.Change[RulesImportResult], error) {
	return ruleCommand(ctx, s, RulesImportName, "", func(tx *sql.Tx) (RulesImportResult, string, state.Outcome, error) {
		n, err := rules.ImportRulesTx(ctx, tx, in.Document)
		return RulesImportResult{Count: n}, "", state.OK, err
	})
}
