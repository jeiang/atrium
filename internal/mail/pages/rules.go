package pages

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var ruleProblems = map[string]string{
	"op eq needs a value":                         "“is” needs a value.",
	"op re needs a value":                         "“matches regex” needs a pattern.",
	"op suffix needs a value":                     "“is or ends in domain” needs at least one domain.",
	"file rule needs dest":                        "Choose a destination, or set the action to Delete.",
	"when must be a non-empty list":               "Add at least one condition.",
	"op in needs a non-empty list of strings":     "“is one of” needs at least one value.",
	"op suffix needs a string or list of strings": "“is or ends in domain” needs at least one domain.",
}

var ruleIDPrefix = rx.MustCompile(`^[^:\s]+: `, 0)

// ruleProblem is rule_problem: the rule id prefix of an error is dropped and the text made friendly.
func ruleProblem(err error) string {
	text := err.Error()
	if loc, _ := ruleIDPrefix.Search(text); loc != nil {
		text = text[loc.End():]
	}
	if friendly, ok := ruleProblems[text]; ok {
		return friendly
	}
	_, size := utf8.DecodeRuneInString(text)
	return py.Upper(text[:size]) + text[size:]
}

func (s *service) ruleViews(ctx context.Context, account string) []ruleView {
	var scope *string
	if account != "" {
		scope = &account
	}
	listed, err := ruleedit.ListRules(ctx, s.deps.DB.Read, scope)
	check(err)
	out := make([]ruleView, len(listed))
	for i, r := range listed {
		out[i] = ruleView{ID: r.ID, Account: r.Account, Action: r.Action, Dest: r.Dest, When: conditionViews(r.When)}
	}
	return out
}

func (s *service) folderMap(ctx context.Context) map[string][]triage.Folder {
	out := map[string][]triage.Folder{}
	for _, spec := range config.Accounts {
		folders, err := triage.Folders(ctx, s.deps.DB.Read, spec.Name)
		check(err)
		out[spec.Name] = folders
	}
	return out
}

func (s *service) learnedFor(ctx context.Context, account string) ([]learnedView, bool) {
	names := []string{account}
	if account == "" {
		names = names[:0]
		for _, spec := range config.Accounts {
			names = append(names, spec.Name)
		}
	}
	var found []learnedView
	capped := false
	for _, name := range names {
		summary, err := ruleedit.LearnedRulesSummary(ctx, s.deps.DB.Read, name, learnedLimit)
		check(err)
		capped = capped || len(summary) == learnedLimit
		for _, l := range summary {
			found = append(found, learnedView{l.Level, l.Key, l.Dest, l.Purity, l.N, name})
		}
	}
	slices.SortStableFunc(found, func(a, b learnedView) int {
		return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(b.Purity, a.Purity), cmp.Compare(a.Level, b.Level), cmp.Compare(a.Key, b.Key))
	})
	return found, capped
}

func (s *service) rulesPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	account, saved, created := web.QueryValue(q, "account"), web.QueryValue(q, "saved"), web.QueryValue(q, "created")
	ctx := r.Context()
	learned, capped := s.learnedFor(ctx, account)
	data := rulesData{Account: account, Rules: s.ruleViews(ctx, account), Learned: learned}
	if capped {
		limit := learnedLimit
		data.LearnedLimit = &limit
	}
	var notice *tpl.Toast
	if saved != "" {
		title := "Rule saved"
		if created != "" {
			title = "Rule created"
		}
		notice = toast("success", title, saved)
	}
	s.page(w, r, http.StatusOK, "rules.gohtml", data, notice)
}

func (s *service) ruleNew(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	account := web.QueryValue(q, "account")
	rule := ruleView{Action: rules.ActionFile, When: []map[string]any{}}
	if account != "" {
		rule.Account = &account
	}
	data := ruleEditData{IsNew: true, Suggestions: []map[string]any{}, Errors: []string{}}
	ctx := r.Context()
	if values, given := q["message"]; given {
		sourceID, ok := py.LaxInt64(web.LastValue(values, ""))
		if !ok {
			web.Unprocessable(w, web.BadInt("query", "message", web.LastValue(values, "")))
			return
		}
		features, err := ruleedit.MessageFeatures(ctx, s.deps.DB.Read, sourceID)
		var refused rules.RuleError
		if errors.As(err, &refused) {
			s.fail(w, r, http.StatusNotFound, refused.Error())
			return
		}
		check(err)
		var source sourceMessage
		check(s.deps.DB.Read.QueryRowContext(ctx, "SELECT id, subject, from_addr FROM messages WHERE id = ?", sourceID).
			Scan(&source.ID, &source.Subject, &source.FromAddr))
		data.SourceMessage = &source
		rule.Account, rule.Dest, rule.When = &features.Account, features.Dest, conditionViews(features.Default)
		for _, c := range features.Conditions {
			if !slices.ContainsFunc(features.Default, func(d rules.Condition) bool { return reflect.DeepEqual(c, d) }) {
				data.Suggestions = append(data.Suggestions, conditionView(c))
			}
		}
	}
	data.Rule, data.Folders = rule, s.folderMap(ctx)
	s.page(w, r, http.StatusOK, "rule_edit.gohtml", data, nil)
}

func (s *service) ruleCondition(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	field, op := "from_addr", "eq"
	if values, given := q["field"]; given {
		field = web.LastValue(values, "")
	}
	if values, given := q["op"]; given {
		op = web.LastValue(values, "")
	}
	c := map[string]any{"field": field, "op": op, "value": web.QueryValue(q, "value")}
	s.fragment(w, r, "_condition_row.gohtml", conditionData{C: c, Focus: web.QueryValue(q, "focus")}, nil)
}

type previewData struct {
	Preview *ruleedit.Preview `json:"preview"`
	Error   *string           `json:"error"`
}

// ruleForm is the fields a rule is edited with.
type ruleForm struct {
	account, action, dest string
	fields, ops, values   []string
}

func readRuleForm(form url.Values) ruleForm {
	return ruleForm{
		account: web.FormString(form, "account", ""), action: web.FormString(form, "action", rules.ActionFile), dest: web.FormString(form, "dest", ""),
		fields: form["field"], ops: form["op"], values: form["value"],
	}
}

// conditionList is condition_list: the conditions a rule form describes, each field, op and value zipped.
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
			list := []string{}
			for part := range strings.SplitSeq(value, ",") {
				if part = py.Strip(part); part != "" {
					list = append(list, part)
				}
			}
			condition["value"] = list
		case field == rules.AgeField && py.IsDigits(py.Strip(value)):
			n, err := py.IntString(value)
			if err != nil {
				panic(err)
			}
			if n.IsInt64() {
				condition["value"] = n.Int64()
			} else {
				condition["value"] = n
			}
		default:
			condition["value"] = value
		}
		out = append(out, condition)
	}
	return out
}

// raw is rule_raw: the rule dict the editor functions take, without its id.
func (f ruleForm) raw() map[string]any {
	var account, dest any
	if f.account != "" {
		account = f.account
	}
	if f.action != rules.ActionDelete && f.dest != "" {
		dest = f.dest
	}
	return map[string]any{"account": account, "action": f.action, "dest": dest, "when": conditionList(f.fields, f.ops, f.values)}
}

func (s *service) rulePreview(w http.ResponseWriter, r *http.Request) {
	raw := readRuleForm(web.FormValues(r)).raw()
	raw["id"] = ruleedit.PreviewID
	found, err := ruleedit.PreviewRule(r.Context(), s.deps.DB.Read, raw, ruleedit.DefaultLimit)
	var invalid rules.RuleError
	if errors.As(err, &invalid) {
		problem := ruleProblem(err)
		s.fragment(w, r, "_rule_preview.gohtml", previewData{Error: &problem}, nil)
		return
	}
	check(err)
	s.fragment(w, r, "_rule_preview.gohtml", previewData{Preview: &found}, nil)
}

// shownRule is the rule dict of a form as the editor shows it again after an error.
func shownRule(id string, raw map[string]any) ruleView {
	view := ruleView{ID: id, Action: raw["action"].(string), When: []map[string]any{}}
	if account, ok := raw["account"].(string); ok {
		view.Account = &account
	}
	if dest, ok := raw["dest"].(string); ok {
		view.Dest = &dest
	}
	for _, c := range raw["when"].([]any) {
		view.When = append(view.When, c.(map[string]any))
	}
	return view
}

func (s *service) saveRule(w http.ResponseWriter, r *http.Request, id string, isNew bool, form ruleForm) {
	ctx := context.WithoutCancel(r.Context())
	raw := form.raw()
	raw["id"] = id
	var rule rules.Rule
	var err error
	if isNew {
		rule, err = ruleedit.CreateRule(ctx, s.deps.DB, raw)
	} else {
		rule, err = ruleedit.UpdateRule(ctx, s.deps.DB, id, raw)
	}
	var invalid rules.RuleError
	if errors.As(err, &invalid) {
		data := ruleEditData{
			Rule: shownRule(id, raw), IsNew: isNew, Folders: s.folderMap(ctx), Suggestions: []map[string]any{},
			Errors: []string{ruleProblem(err)},
		}
		s.page(w, r, http.StatusOK, "rule_edit.gohtml", data, nil)
		return
	}
	check(err)
	var query []string
	for _, p := range [][2]string{{"account", form.account}, {"saved", rule.ID}, {"created", map[bool]string{true: "1"}[isNew]}} {
		if p[1] != "" {
			query = append(query, py.URLQuotePlus(p[0])+"="+py.URLQuotePlus(p[1]))
		}
	}
	http.Redirect(w, r, withQuery("/rules", strings.Join(query, "&")), http.StatusSeeOther)
}

func (s *service) ruleCreate(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	s.saveRule(w, r, py.Strip(web.FormString(form, "id", "")), true, readRuleForm(form))
}

func (s *service) ruleUpdate(w http.ResponseWriter, r *http.Request) {
	s.saveRule(w, r, web.PathParam(r, "rule_id"), false, readRuleForm(web.FormValues(r)))
}

func (s *service) ruleEdit(w http.ResponseWriter, r *http.Request) {
	id := web.PathParam(r, "rule_id")
	ctx := r.Context()
	all := s.ruleViews(ctx, "")
	found := slices.IndexFunc(all, func(v ruleView) bool { return v.ID == id })
	if found < 0 {
		s.fail(w, r, http.StatusNotFound, "unknown rule")
		return
	}
	data := ruleEditData{Rule: all[found], Folders: s.folderMap(ctx), Suggestions: []map[string]any{}, Errors: []string{}}
	s.page(w, r, http.StatusOK, "rule_edit.gohtml", data, nil)
}

func (s *service) ruleMove(w http.ResponseWriter, r *http.Request) {
	id := web.PathParam(r, "rule_id")
	form := web.FormValues(r)
	var errs []web.FieldError
	direction, given := web.FormField(form, "direction")
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
	ordered := idsOf(s.ruleViews(ctx, account))
	index := slices.Index(ordered, id)
	if index < 0 {
		s.fail(w, r, http.StatusNotFound, "unknown rule")
		return
	}
	target := index + 1
	if direction == "up" {
		target = index - 1
	}
	if target >= 0 && target < len(ordered) {
		ordered[index], ordered[target] = ordered[target], ordered[index]
		check(ruleedit.ReorderRules(ctx, s.deps.DB, ordered))
	}
	data := ruleListData{Rules: s.ruleViews(ctx, account), Account: account, Moved: &moved{ID: id, Direction: direction}}
	s.fragment(w, r, "_rule_list.gohtml", data, nil)
}

func idsOf(views []ruleView) []string {
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
	deleted, err := ruleedit.DeleteRule(ctx, s.deps.DB, id)
	check(err)
	notice := toast("error", "Rule not found", id)
	if deleted {
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
