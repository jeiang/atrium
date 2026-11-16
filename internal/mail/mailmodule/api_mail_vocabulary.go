package mailmodule

import (
	"slices"

	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type RuleVocabulary struct {
	Fields  []RuleVocabularyField  `json:"fields"`
	Ops     []RuleVocabularyOp     `json:"ops"`
	Actions []RuleVocabularyAction `json:"actions"`
}

type RuleVocabularyField struct {
	Name  string   `json:"name"`
	Label string   `json:"label"`
	List  bool     `json:"list"`
	Ops   []string `json:"ops"`
}

type RuleVocabularyOp struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
	Value string `json:"value" api:"enum=none|text|list|days"`
}

type RuleVocabularyAction struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Destination bool   `json:"destination"`
}

func ruleVocabulary() RuleVocabulary {
	var out RuleVocabulary
	for _, name := range rules.FieldNames {
		_, list := rules.ListFields[name]
		out.Fields = append(out.Fields, RuleVocabularyField{Name: name, Label: tpl.Label("field", name), List: list, Ops: slices.Clone(rules.Ops)})
	}
	out.Fields = append(out.Fields, RuleVocabularyField{Name: rules.AgeField, Label: tpl.Label("field", rules.AgeField), Ops: []string{rules.AgeOp}})
	for _, name := range append(slices.Clone(rules.Ops), rules.AgeOp) {
		value := "text"
		switch {
		case slices.Contains(rules.ValuelessOps, name):
			value = "none"
		case name == "in" || name == "suffix":
			value = "list"
		case name == rules.AgeOp:
			value = "days"
		}
		hint := ""
		if value != "none" {
			hint = tpl.Label("op_hint", name)
		}
		out.Ops = append(out.Ops, RuleVocabularyOp{Name: name, Label: tpl.Label("op", name), Hint: hint, Value: value})
	}
	out.Actions = []RuleVocabularyAction{
		{Name: string(rules.ActionFile), Label: "File into", Destination: true},
		{Name: string(rules.ActionDelete), Label: "Delete"},
	}
	return out
}
