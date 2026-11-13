package application

import (
	"cmp"
	"context"
	"math/big"
	"reflect"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/triage"
)

const RulesName application.Name = "mail.rules"
const LearnedLimit = 50

type RulesInput struct {
	Account, ID            string
	MessageID              *int64
	Form, Learned, Folders bool
	Condition              *ConditionResult
}
type RulesRead struct {
	List      RulesResult
	Edit      RuleEdit
	Folders   map[string][]triage.Folder
	Condition *ConditionResult
}
type ConditionView struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

type RuleView struct {
	ID      string          `json:"id"`
	Account *string         `json:"account"`
	Action  string          `json:"action"`
	Dest    *string         `json:"dest"`
	When    []ConditionView `json:"when"`
}

type LearnedView struct {
	Level   string  `json:"level"`
	Key     string  `json:"key"`
	Dest    string  `json:"dest"`
	Purity  float64 `json:"purity"`
	N       int     `json:"n"`
	Account string  `json:"account"`
}

type RulesResult struct {
	Account      string        `json:"account"`
	Rules        []RuleView    `json:"rules"`
	Learned      []LearnedView `json:"learned"`
	LearnedLimit *int          `json:"learned_limit"`
}

type SourceMessage struct {
	ID       int64  `json:"id"`
	Subject  string `json:"subject"`
	FromAddr string `json:"from_addr"`
}

type RuleEdit struct {
	Rule          RuleView                   `json:"rule"`
	IsNew         bool                       `json:"is_new"`
	Folders       map[string][]triage.Folder `json:"folders"`
	Suggestions   []ConditionView            `json:"suggestions"`
	SourceMessage *SourceMessage             `json:"source_message"`
	Errors        []string                   `json:"errors"`
}

type ConditionResult struct {
	C     ConditionView `json:"c"`
	Focus string        `json:"focus"`
}

func ConditionViewOf(c rules.Condition) ConditionView {
	out := ConditionView{Field: c.Field(), Op: c.Op()}
	value, ok := c.Value()
	if !ok {
		return out
	}
	if n, ok := value.(*big.Int); ok {
		out.Value = n.String()
	} else {
		out.Value = value
	}
	return out
}

func ConditionViews(cs []rules.Condition) []ConditionView {
	out := make([]ConditionView, len(cs))
	for i, c := range cs {
		out[i] = ConditionViewOf(c)
	}
	return out
}

func DescribeRules() application.Definition {
	return application.Describe[RulesInput, RulesRead](RulesName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func (s *Service) Rules(ctx context.Context, in RulesInput) (application.Read[RulesRead], error) {
	var out RulesRead
	rev, err := s.snapshot(ctx, RulesName, in, func(q db.Querier) error {
		out.Condition = in.Condition
		if in.Condition != nil {
			return nil
		}
		var scope *string
		if in.Account != "" {
			scope = &in.Account
		}
		listed, err := ruleedit.ListRules(ctx, q, scope)
		if err != nil {
			return err
		}
		out.List.Account = in.Account
		out.List.Rules = make([]RuleView, len(listed))
		for i, r := range listed {
			out.List.Rules[i] = RuleView{ID: r.ID, Account: r.Account, Action: r.Action, Dest: r.Dest, When: ConditionViews(r.When)}
		}
		if in.Learned {
			names := []string{in.Account}
			if in.Account == "" {
				names = names[:0]
				for _, spec := range config.From(ctx).Accounts() {
					names = append(names, spec.Name)
				}
			}
			for _, name := range names {
				summary, err := ruleedit.LearnedRulesSummary(ctx, q, name, LearnedLimit)
				if err != nil {
					return err
				}
				if len(summary) == LearnedLimit {
					limit := LearnedLimit
					out.List.LearnedLimit = &limit
				}
				for _, l := range summary {
					out.List.Learned = append(out.List.Learned, LearnedView{l.Level, l.Key, l.Dest, l.Purity, l.N, name})
				}
			}
			slices.SortStableFunc(out.List.Learned, func(a, b LearnedView) int {
				return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(b.Purity, a.Purity), cmp.Compare(a.Level, b.Level), cmp.Compare(a.Key, b.Key))
			})
		}
		if in.Folders || in.Form {
			out.Folders = map[string][]triage.Folder{}
			for _, spec := range config.From(ctx).Accounts() {
				folders, err := triage.Folders(ctx, q, spec.Name)
				if err != nil {
					return err
				}
				out.Folders[spec.Name] = folders
			}
		}
		if !in.Form {
			return nil
		}
		out.Edit = RuleEdit{IsNew: in.ID == "", Rule: RuleView{Action: string(rules.ActionFile), When: []ConditionView{}}, Folders: out.Folders, Suggestions: []ConditionView{}, Errors: []string{}}
		if in.Account != "" {
			out.Edit.Rule.Account = &in.Account
		}
		if in.ID != "" {
			index := slices.IndexFunc(out.List.Rules, func(r RuleView) bool { return r.ID == in.ID })
			if index < 0 {
				return &application.Error{Code: application.NotFound, Message: "unknown rule"}
			}
			out.Edit.Rule = out.List.Rules[index]
		}
		if in.MessageID != nil {
			features, err := ruleedit.MessageFeatures(ctx, q, *in.MessageID)
			if err != nil {
				return err
			}
			var source SourceMessage
			if err = q.QueryRowContext(ctx, "SELECT id, subject, from_addr FROM messages WHERE id = ?", *in.MessageID).Scan(&source.ID, &source.Subject, &source.FromAddr); err != nil {
				return err
			}
			out.Edit.SourceMessage = &source
			out.Edit.Rule.Account, out.Edit.Rule.Dest, out.Edit.Rule.When = &features.Account, features.Dest, ConditionViews(features.Default)
			for _, c := range features.Conditions {
				if !slices.ContainsFunc(features.Default, func(d rules.Condition) bool { return reflect.DeepEqual(c, d) }) {
					out.Edit.Suggestions = append(out.Edit.Suggestions, ConditionViewOf(c))
				}
			}
		}
		return nil
	})
	return application.Read[RulesRead]{Revision: rev, Data: out}, err
}
