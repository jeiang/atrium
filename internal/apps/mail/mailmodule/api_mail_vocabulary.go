package mailmodule

import (
	"slices"

	"github.com/jeiang/atrium/internal/apps/mail/rules"
)

var ruleFieldLabels = map[string]string{
	"from_addr":    "From address",
	"from_domain":  "From domain",
	"from_name":    "From name",
	"reply_to":     "Reply-To",
	"rpath_domain": "Return-Path domain",
	"list_id":      "List-Id",
	"subject":      "Subject",
	"subject_tmpl": "Subject template",
	"msgid_domain": "Message-ID domain",
	"precedence":   "Precedence",
	"category":     "Category",
	"to":           "To",
	"dkim_d":       "DKIM domain",
	"esp":          "Sending service",
	"att_type":     "Attachment type",
	"att_name":     "Attachment name",
	"age_days":     "Age in days",
}

var ruleOpLabels = map[string]string{
	"eq":      "is",
	"in":      "is one of",
	"suffix":  "is or ends in domain",
	"re":      "matches regex",
	"present": "is present",
	"absent":  "is absent",
	"gte":     "is at least",
}

var ruleOpHints = map[string]string{
	"eq":     "Exact value, case-insensitive",
	"in":     "Values separated by commas",
	"suffix": "Domains separated by commas",
	"re":     "RE2 regular expression, case-insensitive",
	"gte":    "Whole number of days",
}

func ruleLabel(labels map[string]string, key string) string {
	if label, ok := labels[key]; ok {
		return label
	}
	return key
}

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
		out.Fields = append(out.Fields, RuleVocabularyField{Name: name, Label: ruleLabel(ruleFieldLabels, name), List: list, Ops: slices.Clone(rules.Ops)})
	}
	out.Fields = append(out.Fields, RuleVocabularyField{Name: rules.AgeField, Label: ruleLabel(ruleFieldLabels, rules.AgeField), Ops: []string{rules.AgeOp}})
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
			hint = ruleLabel(ruleOpHints, name)
		}
		out.Ops = append(out.Ops, RuleVocabularyOp{Name: name, Label: ruleLabel(ruleOpLabels, name), Hint: hint, Value: value})
	}
	out.Actions = []RuleVocabularyAction{
		{Name: string(rules.ActionFile), Label: "File into", Destination: true},
		{Name: string(rules.ActionDelete), Label: "Delete"},
	}
	return out
}
