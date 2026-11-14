package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"regexp"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var ruleProblems = map[string]string{
	"op-eq-value":     "“is” needs a value.",
	"op-re-value":     "“matches regex” needs a pattern.",
	"op-suffix-value": "“is or ends in domain” needs at least one domain.",
	"file-dest":       "Choose a destination, or set the action to Delete.",
	"when-empty":      "Add at least one condition.",
	"op-in-value":     "“is one of” needs at least one value.",
}

var ruleIDPrefix = regexp.MustCompile(`^[^:\s]+: `)

func ruleProblem(err error) string {
	var refused rules.RuleError
	if errors.As(err, &refused) {
		if friendly, ok := ruleProblems[refused.Code]; ok {
			return friendly
		}
	}
	text := err.Error()
	if loc := ruleIDPrefix.FindStringIndex(text); loc != nil {
		text = text[loc[1]:]
	}
	_, size := utf8.DecodeRuneInString(text)
	return strings.ToUpper(text[:size]) + text[size:]
}

func (s *service) ruleViews(ctx context.Context, account string) []mailapp.RuleView {
	read, err := s.reads.Rules(ctx, mailapp.RulesInput{Account: account})
	check(err)
	return read.Data.List.Rules
}
func (s *service) folderMap(ctx context.Context) map[string][]triage.Folder {
	read, err := s.reads.Rules(ctx, mailapp.RulesInput{Folders: true})
	check(err)
	return read.Data.Folders
}

func (s *service) rulesPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	account, saved, created := web.QueryValue(q, "account"), web.QueryValue(q, "saved"), web.QueryValue(q, "created")
	ctx := r.Context()
	read, err := s.reads.Rules(ctx, mailapp.RulesInput{Account: account, Learned: true})
	check(err)
	data := read.Data.List
	var notice *tpl.Toast
	if saved != "" {
		title := "Rule saved"
		if created != "" {
			title = "Rule created"
		}
		notice = toast("success", title, saved)
	}
	s.page(w, r, http.StatusOK, "rules", "rules.gohtml", data, notice)
}

func (s *service) ruleNew(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := mailapp.RulesInput{Account: web.QueryValue(q, "account"), Form: true}
	if _, given := q["message"]; given {
		sourceID, ok := laxInt64(web.ValueOr(q, "message", ""))
		if !ok {
			web.Unprocessable(w, web.BadInt("query", "message", web.ValueOr(q, "message", "")))
			return
		}
		in.MessageID = &sourceID
	}
	read, err := s.reads.Rules(r.Context(), in)
	var refused rules.RuleError
	if errors.As(err, &refused) {
		s.fail(w, r, http.StatusNotFound, refused.Error())
		return
	}
	check(err)
	s.page(w, r, http.StatusOK, "rules", "rule_edit.gohtml", read.Data.Edit, nil)
}

func (s *service) ruleCondition(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	field, op := "from_addr", "eq"
	if _, given := q["field"]; given {
		field = web.ValueOr(q, "field", "")
	}
	if _, given := q["op"]; given {
		op = web.ValueOr(q, "op", "")
	}
	c := mailapp.ConditionView{Field: field, Op: op, Value: web.QueryValue(q, "value")}
	read, err := s.reads.Rules(r.Context(), mailapp.RulesInput{Condition: &mailapp.ConditionResult{C: c, Focus: web.QueryValue(q, "focus")}})
	check(err)
	if web.FragmentNotModified(w, r, read.Revision) {
		return
	}
	check(s.deps.Render.Fragment(w, r, http.StatusOK, "_condition_row.gohtml", tpl.Fragment{Data: read.Data.Condition}))
}

type previewData struct {
	Preview *ruleedit.Preview `json:"preview"`
	Error   *string           `json:"error"`
}

type ruleForm struct {
	account, action, dest string
	fields, ops, values   []string
}

func readRuleForm(form url.Values) ruleForm {
	return ruleForm{
		account: web.FormString(form, "account", ""), action: web.FormString(form, "action", string(rules.ActionFile)), dest: web.FormString(form, "dest", ""),
		fields: form["field"], ops: form["op"], values: form["value"],
	}
}

func splitList(value string) []string {
	out := []string{}
	for line := range strings.SplitSeq(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func conditionList(fields, ops, values []string) []any {
	out := []any{}
	for i := range min(len(fields), len(ops)) {
		field, op, value := fields[i], ops[i], ""
		if i < len(values) {
			value = values[i]
		}
		condition := map[string]any{"field": field, "op": op}
		switch {
		case slices.Contains(rules.ValuelessOps, op):
		case op == "in" || op == "suffix":
			condition["value"] = splitList(value)
		case field == rules.AgeField && isDigits(strings.TrimSpace(value)):
			n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				panic(err)
			}
			condition["value"] = n
		default:
			condition["value"] = value
		}
		out = append(out, condition)
	}
	return out
}

func conditionViewsFromForm(fields, ops, values []string) []mailapp.ConditionView {
	out := []mailapp.ConditionView{}
	for i := range min(len(fields), len(ops)) {
		field, op, value := fields[i], ops[i], ""
		if i < len(values) {
			value = values[i]
		}
		view := mailapp.ConditionView{Field: field, Op: op}
		switch {
		case slices.Contains(rules.ValuelessOps, op):
		case op == "in" || op == "suffix":
			view.Value = splitList(value)
		case field == rules.AgeField && isDigits(strings.TrimSpace(value)):
			n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				panic(err)
			}
			view.Value = n
		default:
			view.Value = value
		}
		out = append(out, view)
	}
	return out
}

func (f ruleForm) raw() map[string]any {
	var account, dest any
	if f.account != "" {
		account = f.account
	}
	if rules.Action(f.action) != rules.ActionDelete && f.dest != "" {
		dest = f.dest
	}
	return map[string]any{"account": account, "action": f.action, "dest": dest, "when": conditionList(f.fields, f.ops, f.values)}
}

func (f ruleForm) view(id string) mailapp.RuleView {
	view := mailapp.RuleView{ID: id, Action: f.action, When: conditionViewsFromForm(f.fields, f.ops, f.values)}
	if f.account != "" {
		account := f.account
		view.Account = &account
	}
	if rules.Action(f.action) != rules.ActionDelete && f.dest != "" {
		dest := f.dest
		view.Dest = &dest
	}
	return view
}

func (s *service) rulePreview(w http.ResponseWriter, r *http.Request) {
	raw := readRuleForm(web.FormValues(r)).raw()
	raw["id"] = ruleedit.PreviewID
	read, err := s.reads.RulePreview(r.Context(), mailapp.RulePreviewInput{Rule: raw})
	var invalid rules.RuleError
	if errors.As(err, &invalid) {
		problem := ruleProblem(err)
		s.fragment(w, r, "_rule_preview.gohtml", previewData{Error: &problem}, nil)
		return
	}
	check(err)
	s.fragment(w, r, "_rule_preview.gohtml", previewData{Preview: &read.Data}, nil)
}

func (s *service) saveRule(w http.ResponseWriter, r *http.Request, id string, isNew bool, form ruleForm) {
	ctx := context.WithoutCancel(r.Context())
	raw := form.raw()
	raw["id"] = id
	save := s.reads.RuleUpdate
	if isNew {
		save = s.reads.RuleCreate
	}
	done, err := save(ctx, mailapp.RuleInput{ID: id, Rule: raw})
	rule := done.Data
	var invalid rules.RuleError
	if errors.As(err, &invalid) {
		data := mailapp.RuleEdit{
			Rule: form.view(id), IsNew: isNew, Folders: s.folderMap(ctx), Suggestions: []mailapp.ConditionView{},
			Errors: []string{ruleProblem(err)},
		}
		s.page(w, r, http.StatusOK, "rules", "rule_edit.gohtml", data, nil)
		return
	}
	check(err)
	var query []string
	for _, p := range [][2]string{{"account", form.account}, {"saved", rule.ID}, {"created", map[bool]string{true: "1"}[isNew]}} {
		if p[1] != "" {
			query = append(query, url.QueryEscape(p[0])+"="+url.QueryEscape(p[1]))
		}
	}
	http.Redirect(w, r, withQuery("/rules", strings.Join(query, "&")), http.StatusSeeOther)
}

func (s *service) ruleCreate(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	s.saveRule(w, r, strings.TrimSpace(web.FormString(form, "id", "")), true, readRuleForm(form))
}

func (s *service) ruleUpdate(w http.ResponseWriter, r *http.Request) {
	s.saveRule(w, r, web.PathParam(r, "rule_id"), false, readRuleForm(web.FormValues(r)))
}

func (s *service) ruleEdit(w http.ResponseWriter, r *http.Request) {
	read, err := s.reads.Rules(r.Context(), mailapp.RulesInput{ID: web.PathParam(r, "rule_id"), Form: true})
	var problem *application.Error
	if errors.As(err, &problem) && problem.Code == application.NotFound {
		s.fail(w, r, http.StatusNotFound, "unknown rule")
		return
	}
	check(err)
	s.page(w, r, http.StatusOK, "rules", "rule_edit.gohtml", read.Data.Edit, nil)
}

func (s *service) ruleMove(w http.ResponseWriter, r *http.Request) {
	id := web.PathParam(r, "rule_id")
	form := web.FormValues(r)
	var errs []web.FieldError
	direction, given := web.Value(form, "direction")
	if given && direction == "" {
		given = false
	}
	switch {
	case !given:
		errs = append(errs, web.Missing("body", "direction"))
	case direction != "up" && direction != "down":
		errs = append(errs, web.BadLiteral("body", "direction", direction, "'up'", "'down'"))
	}
	if errs != nil {
		web.Unprocessable(w, errs...)
		return
	}
	account := web.FormString(form, "account", "")
	ctx := context.WithoutCancel(r.Context())
	_, err := s.reads.RuleReorder(ctx, mailapp.RuleReorderInput{ID: id, Account: account, Direction: direction})
	var unknown *application.Error
	if errors.As(err, &unknown) && unknown.Code == application.NotFound {
		s.fail(w, r, http.StatusNotFound, "unknown rule")
		return
	}
	check(err)
	data := ruleListData{Rules: s.ruleViews(ctx, account), Account: account, Moved: &moved{ID: id, Direction: direction}}
	s.fragment(w, r, "_rule_list.gohtml", data, nil)
}

func idsOf(views []mailapp.RuleView) []string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.ID
	}
	return out
}

func (s *service) ruleDelete(w http.ResponseWriter, r *http.Request) {
	id := web.PathParam(r, "rule_id")
	account := web.QueryValue(r.URL.Query(), "account")
	ctx := context.WithoutCancel(r.Context())
	before := idsOf(s.ruleViews(ctx, account))
	done, err := s.reads.RuleDelete(ctx, mailapp.RuleDeleteInput{ID: id})
	check(err)
	notice := toast("error", "Rule not found", id)
	if done.Data.Deleted {
		notice = toast("success", "Rule deleted", id)
	}
	remaining := s.ruleViews(ctx, account)
	data := ruleListData{Rules: remaining, Account: account}
	if len(remaining) > 0 {
		index := max(slices.Index(before, id), 0)
		data.FocusID = &remaining[min(index, len(remaining)-1)].ID
	}
	s.fragment(w, r, "_rule_list.gohtml", data, notice)
}
