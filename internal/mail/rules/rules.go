// Package rules is src/atrium/rules.py: the operator's routing rules, their
// validation and JSON import/export, and the learned rules derived from
// what is already filed.
package rules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	AgeField      = "age_days"
	AgeOp         = "gte"
	secondsPerDay = 86400
	ActionFile    = "file"
	ActionDelete  = "delete"
)

var (
	// ListFields maps a list-valued rule field to its messages column.
	ListFields = map[string]string{
		"to":       "to_addrs",
		"dkim_d":   "dkim_d",
		"esp":      "esp",
		"att_type": "att_types",
		"att_name": "att_names",
	}
	// ScalarFields maps a scalar rule field to its messages column.
	ScalarFields = map[string]string{
		"from_addr":    "from_addr",
		"from_domain":  "from_domain",
		"from_name":    "from_name",
		"reply_to":     "reply_to",
		"rpath_domain": "rpath_domain",
		"list_id":      "list_id",
		"subject":      "subject",
		"subject_tmpl": "subject_tmpl",
		"msgid_domain": "msgid_domain",
		"precedence":   "precedence",
		"category":     "category",
	}
	// FieldNames is FIELDS in Python's iteration order: the scalar fields, then the list fields.
	FieldNames = []string{
		"from_addr", "from_domain", "from_name", "reply_to", "rpath_domain", "list_id", "subject", "subject_tmpl",
		"msgid_domain", "precedence", "category", "to", "dkim_d", "esp", "att_type", "att_name",
	}
	Ops          = []string{"eq", "in", "suffix", "re", "present", "absent"}
	ValuelessOps = []string{"present", "absent"}
	Actions      = []string{ActionFile, ActionDelete}
)

// LearnedLevel is one precision level of the learned rules.
type LearnedLevel struct {
	Name    string
	Columns []string
}

var LearnedLevels = []LearnedLevel{
	{"addr", []string{"from_addr"}},
	{"addr+tmpl", []string{"from_addr", "subject_tmpl"}},
	{"domain", []string{"from_domain"}},
}

// RuleError is Python's RuleError, a ValueError whose text is shown to the operator.
type RuleError string

func (e RuleError) Error() string { return string(e) }

func ruleErrorf(format string, args ...any) error { return RuleError(fmt.Sprintf(format, args...)) }

// Condition is one `when` entry. Value is nil, a string, a []string, an int64, a *big.Int or a float64;
// HasValue tells an absent value from a null one.
type Condition struct {
	Field    string
	Op       string
	Value    any
	HasValue bool
}

// Rule is one operator rule; Account and Dest are nil where Python has None.
type Rule struct {
	ID         string
	Account    *string
	Action     string
	Dest       *string
	Conditions []Condition
}

// JSON is json.dumps of the condition as the rules table stores it.
func (c Condition) JSON() string {
	s := `{"field": ` + py.JSONDumps(c.Field) + `, "op": ` + py.JSONDumps(c.Op)
	if c.HasValue {
		s += `, "value": ` + py.JSONDumps(c.Value)
	}
	return s + "}"
}

// MarshalJSON writes the condition as the Python dict: field, op and, when present, value.
func (c Condition) MarshalJSON() ([]byte, error) { return []byte(c.JSON()), nil }

// ConditionsJSON is json.dumps of a rule's conditions, the text of rules.conditions.
func ConditionsJSON(conds []Condition) string {
	parts := make([]string, len(conds))
	for i, c := range conds {
		parts[i] = c.JSON()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Message is the columns of a messages row that rules, the router and the rule editor read. The list columns
// keep the JSON text stored in the database.
type Message struct {
	ID          int64
	Account     string
	FromAddr    string
	FromDomain  string
	FromName    string
	ReplyTo     string
	RpathDomain string
	ListID      string
	Subject     string
	SubjectTmpl string
	MsgidDomain string
	Precedence  string
	Category    string
	ToAddrs     string
	DKIMD       string
	ESP         string
	AttTypes    string
	AttNames    string
	DateTS      float64
	InInbox     bool
	Embedded    bool
	Folder      string
	Location    string
}

// MessageColumns is the SELECT list ScanMessage reads.
const MessageColumns = "id, account, from_addr, from_domain, from_name, reply_to, rpath_domain, list_id, subject, " +
	"subject_tmpl, msgid_domain, precedence, category, to_addrs, dkim_d, esp, att_types, att_names, date_ts, " +
	"in_inbox, embedded, COALESCE(folder, ''), location"

// Scanner is *sql.Row or *sql.Rows.
type Scanner interface{ Scan(dest ...any) error }

// ScanMessage reads one row selected with MessageColumns.
func ScanMessage(s Scanner) (Message, error) {
	var m Message
	var dateTS sql.NullFloat64
	var inbox, embedded int64
	err := s.Scan(&m.ID, &m.Account, &m.FromAddr, &m.FromDomain, &m.FromName, &m.ReplyTo, &m.RpathDomain, &m.ListID,
		&m.Subject, &m.SubjectTmpl, &m.MsgidDomain, &m.Precedence, &m.Category, &m.ToAddrs, &m.DKIMD, &m.ESP,
		&m.AttTypes, &m.AttNames, &dateTS, &inbox, &embedded, &m.Folder, &m.Location)
	m.DateTS = dateTS.Float64
	m.InInbox = inbox != 0
	m.Embedded = embedded != 0
	return m, err
}

func (m *Message) scalar(field string) string {
	switch field {
	case "from_addr":
		return m.FromAddr
	case "from_domain":
		return m.FromDomain
	case "from_name":
		return m.FromName
	case "reply_to":
		return m.ReplyTo
	case "rpath_domain":
		return m.RpathDomain
	case "list_id":
		return m.ListID
	case "subject":
		return m.Subject
	case "subject_tmpl":
		return m.SubjectTmpl
	case "msgid_domain":
		return m.MsgidDomain
	case "precedence":
		return m.Precedence
	}
	return m.Category
}

func (m *Message) list(field string) string {
	switch field {
	case "to":
		return m.ToAddrs
	case "dkim_d":
		return m.DKIMD
	case "esp":
		return m.ESP
	case "att_type":
		return m.AttTypes
	}
	return m.AttNames
}

// FieldValues is rules.field_values: the values a condition on field looks at.
func FieldValues(m *Message, field string) ([]string, error) {
	if _, ok := ListFields[field]; ok {
		raw, err := py.JSONLoads([]byte(m.list(field)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ListFields[field], err)
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: %w", ListFields[field], errNotList)
		}
		out := make([]string, len(items))
		for i, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s: %w", ListFields[field], errNotStr)
			}
			out[i] = s
		}
		return out, nil
	}
	return []string{m.scalar(field)}, nil
}

var (
	errNotList = errors.New("expected a JSON list")
	errNotStr  = errors.New("expected a list of strings")
)

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func ageMatches(m *Message, days float64) bool {
	return m.DateTS != 0 && (now()-m.DateTS)/secondsPerDay >= days
}

// DomainSuffix is rules.domain_suffix.
func DomainSuffix(value, suffix string) bool {
	return value == suffix || strings.HasSuffix(value, "."+suffix)
}

func members(v any) ([]string, error) {
	switch v := v.(type) {
	case string:
		return []string{v}, nil
	case []string:
		return v, nil
	case []any:
		out := make([]string, len(v))
		for i, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("condition value: %w", errNotStr)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("condition value: %w", errNotStr)
}

func number(v any) (float64, bool) {
	switch v := v.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case *big.Int:
		f, _ := new(big.Float).SetInt(v).Float64()
		return f, true
	}
	return 0, false
}

// Pattern compiles the pattern of a `re` condition as condition_matches does.
func Pattern(value string) (*rx.Regexp, error) { return rx.Compile(value, rx.IgnoreCase) }

// CheckPattern compiles value as validate_condition does.
func CheckPattern(value string) (*rx.Regexp, error) { return rx.Compile(value, 0) }

// Matches is rules.condition_matches. A match attempt that times out is a non-match; any other error is a stored
// condition or message column that Python would also fail on.
func (c Condition) Matches(m *Message) (bool, error) {
	if c.Field == AgeField {
		days, ok := number(c.Value)
		if !ok {
			return false, fmt.Errorf("age_days value %s: not a number", py.ReprValue(c.Value))
		}
		return ageMatches(m, days), nil
	}
	values, err := FieldValues(m, c.Field)
	if err != nil {
		return false, err
	}
	switch c.Op {
	case "present":
		return anyNonEmpty(values), nil
	case "absent":
		return !anyNonEmpty(values), nil
	case "eq":
		target, ok := c.Value.(string)
		if !ok {
			return false, fmt.Errorf("op eq: value %s: not a string", py.ReprValue(c.Value))
		}
		target = py.Casefold(target)
		for _, v := range values {
			if py.Casefold(v) == target {
				return true, nil
			}
		}
		return false, nil
	case "in":
		list, err := members(c.Value)
		if err != nil {
			return false, err
		}
		targets := map[string]bool{}
		for _, t := range list {
			targets[py.Casefold(t)] = true
		}
		for _, v := range values {
			if targets[py.Casefold(v)] {
				return true, nil
			}
		}
		return false, nil
	case "suffix":
		list, err := members(c.Value)
		if err != nil {
			return false, err
		}
		targets := make([]string, len(list))
		for i, t := range list {
			targets[i] = py.Casefold(t)
		}
		for _, v := range values {
			if v == "" {
				continue
			}
			folded := py.Casefold(v)
			for _, t := range targets {
				if DomainSuffix(folded, t) {
					return true, nil
				}
			}
		}
		return false, nil
	}
	pattern, ok := c.Value.(string)
	if !ok {
		return false, fmt.Errorf("op re: value %s: not a string", py.ReprValue(c.Value))
	}
	re, err := Pattern(pattern)
	if err != nil {
		return false, err
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		if found, _ := re.Search(v); found != nil {
			return true, nil
		}
	}
	return false, nil
}

func anyNonEmpty(values []string) bool {
	for _, v := range values {
		if v != "" {
			return true
		}
	}
	return false
}

// Matches is rules.rule_matches: every condition holds.
func (r Rule) Matches(m *Message) (bool, error) {
	for _, c := range r.Conditions {
		ok, err := c.Matches(m)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// FirstMatch is rules.first_match; nil when no rule matches.
func FirstMatch(m *Message, rules []Rule) (*Rule, error) {
	for i := range rules {
		ok, err := rules[i].Matches(m)
		if err != nil {
			return nil, err
		}
		if ok {
			return &rules[i], nil
		}
	}
	return nil, nil
}

// object reads a JSON object given as py.Dict or map[string]any.
func object(v any) (get func(string) (any, bool), ok bool) {
	switch v := v.(type) {
	case py.Dict:
		return v.Get, true
	case map[string]any:
		return func(k string) (any, bool) { x, found := v[k]; return x, found }, true
	}
	return nil, false
}

func isString(v any) (string, bool) { s, ok := v.(string); return s, ok }

func valueList(v any) ([]string, bool) {
	var items []any
	switch v := v.(type) {
	case []string:
		items = make([]any, len(v))
		for i, s := range v {
			items[i] = s
		}
	case []any:
		items = v
	default:
		return nil, false
	}
	if len(items) == 0 {
		return nil, false
	}
	out := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok || py.Strip(s) == "" {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

func inList(s string, list []string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// ValidateCondition is rules.validate_condition for one decoded JSON value.
func ValidateCondition(raw any) (Condition, error) {
	get, ok := object(raw)
	if !ok {
		return Condition{}, ruleErrorf("condition must be an object: %s", py.ReprValue(raw))
	}
	fieldV, _ := get("field")
	opV, _ := get("op")
	value, _ := get("value")
	field, fieldIsStr := isString(fieldV)
	op, opIsStr := isString(opV)
	if fieldIsStr && field == AgeField {
		days, isNum := number(value)
		if _, isBool := value.(bool); !opIsStr || op != AgeOp || isBool || !isNum || days <= 0 {
			return Condition{}, ruleErrorf("%s needs op %s and a positive number of days", AgeField, AgeOp)
		}
		return Condition{field, op, value, true}, nil
	}
	if !fieldIsStr || (ListFields[field] == "" && ScalarFields[field] == "") {
		return Condition{}, ruleErrorf("unknown field %s", py.ReprValue(fieldV))
	}
	if !opIsStr || !inList(op, Ops) {
		return Condition{}, ruleErrorf("unknown op %s", py.ReprValue(opV))
	}
	if inList(op, ValuelessOps) {
		if value != nil {
			return Condition{}, ruleErrorf("op %s takes no value", op)
		}
		return Condition{Field: field, Op: op}, nil
	}
	text, isStr := value.(string)
	if (op == "eq" || op == "re") && !isStr {
		return Condition{}, ruleErrorf("op %s needs a string value", op)
	}
	if isStr && py.Strip(text) == "" {
		return Condition{}, ruleErrorf("op %s needs a value", op)
	}
	list, isList := valueList(value)
	if op == "in" && !isList {
		return Condition{}, ruleErrorf("op in needs a non-empty list of strings")
	}
	if op == "suffix" && !isStr && !isList {
		return Condition{}, ruleErrorf("op suffix needs a string or list of strings")
	}
	if op == "re" {
		if _, err := CheckPattern(text); err != nil {
			return Condition{}, ruleErrorf("invalid regex %s: %s", py.Repr(text), err)
		}
	}
	if isList {
		value = list
	}
	return Condition{field, op, value, true}, nil
}

// ValidateRule is rules.validate_rule for one decoded JSON value.
func ValidateRule(raw any) (Rule, error) {
	get, ok := object(raw)
	if !ok {
		return Rule{}, ruleErrorf("rule must be an object")
	}
	idV, _ := get("id")
	id, ok := idV.(string)
	if !ok || id == "" {
		return Rule{}, ruleErrorf("rule needs a string id")
	}
	action := any(ActionFile)
	if v, found := get("action"); found {
		action = v
	}
	act, _ := action.(string)
	if !inList(act, Actions) {
		return Rule{}, ruleErrorf("%s: unknown action %s", id, py.ReprValue(action))
	}
	destV, _ := get("dest")
	dest, destIsStr := destV.(string)
	if act == ActionFile && (!destIsStr || dest == "") {
		return Rule{}, ruleErrorf("%s: file rule needs dest", id)
	}
	if act == ActionDelete && destV != nil {
		return Rule{}, ruleErrorf("%s: delete rule takes no dest", id)
	}
	accountV, _ := get("account")
	account, accountIsStr := accountV.(string)
	if accountV != nil && !accountIsStr {
		return Rule{}, ruleErrorf("%s: account must be a string", id)
	}
	whenV, _ := get("when")
	var items []any
	switch w := whenV.(type) {
	case []any:
		items = w
	case []string:
		for _, s := range w {
			items = append(items, s)
		}
	}
	if len(items) == 0 {
		return Rule{}, ruleErrorf("%s: when must be a non-empty list", id)
	}
	rule := Rule{ID: id, Action: act}
	if accountIsStr {
		rule.Account = &account
	}
	if destIsStr {
		rule.Dest = &dest
	}
	for _, item := range items {
		c, err := ValidateCondition(item)
		if err != nil {
			return Rule{}, err
		}
		rule.Conditions = append(rule.Conditions, c)
	}
	return rule, nil
}

// ParseRuleset is rules.parse_ruleset: a list of rules or {"rules": [...]}.
func ParseRuleset(document any) ([]Rule, error) {
	list := document
	if get, ok := object(document); ok {
		list, _ = get("rules")
	}
	items, ok := list.([]any)
	if !ok {
		return nil, ruleErrorf(`expected a list of rules or {"rules": [...]}`)
	}
	parsed := make([]Rule, 0, len(items))
	ids := map[string]bool{}
	for _, item := range items {
		r, err := ValidateRule(item)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, r)
		ids[r.ID] = true
	}
	if len(ids) != len(parsed) {
		return nil, ruleErrorf("duplicate rule ids")
	}
	return parsed, nil
}

func nullable(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// ImportRules replaces the rules table with the document's rules in one transaction and returns their count.
func ImportRules(ctx context.Context, d *db.DB, document any) (int, error) {
	parsed, err := ParseRuleset(document)
	if err != nil {
		return 0, err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM rules"); err != nil {
			return err
		}
		for i, r := range parsed {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (?, ?, ?, ?, ?, ?)",
				i, r.ID, nullable(r.Account), r.Action, nullable(r.Dest), ConditionsJSON(r.Conditions)); err != nil {
				return err
			}
		}
		return nil
	})
	return len(parsed), err
}

// Querier is the read side of *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadRules(ctx context.Context, q Querier, keep func(account sql.NullString) bool) ([]Rule, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, account, action, dest, conditions FROM rules ORDER BY position")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Rule
	for rows.Next() {
		var id, action, conds string
		var account, dest sql.NullString
		if err := rows.Scan(&id, &account, &action, &dest, &conds); err != nil {
			return nil, err
		}
		if !keep(account) {
			continue
		}
		r := Rule{ID: id, Action: action}
		if account.Valid {
			r.Account = &account.String
		}
		if dest.Valid {
			r.Dest = &dest.String
		}
		if r.Conditions, err = parseStored(conds); err != nil {
			return nil, fmt.Errorf("rule %s: %w", id, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func parseStored(text string) ([]Condition, error) {
	raw, err := py.JSONLoads([]byte(text))
	if err != nil {
		return nil, err
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errNotList
	}
	out := make([]Condition, len(items))
	for i, item := range items {
		d, ok := item.(py.Dict)
		if !ok {
			return nil, fmt.Errorf("condition %s: not an object", py.ReprValue(item))
		}
		field, _ := d.Get("field")
		op, _ := d.Get("op")
		value, has := d.Get("value")
		f, fok := field.(string)
		o, ook := op.(string)
		if !fok || !ook {
			return nil, fmt.Errorf("condition %s: field and op must be strings", py.ReprValue(item))
		}
		if list, ok := value.([]any); ok {
			if strs, ok := valueListLoose(list); ok {
				value = strs
			}
		}
		out[i] = Condition{f, o, value, has}
	}
	return out, nil
}

func valueListLoose(list []any) ([]string, bool) {
	out := make([]string, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// LoadRules is rules.load_rules(conn, account): the rules scoped to account or to every account, by position.
func LoadRules(ctx context.Context, q Querier, account string) ([]Rule, error) {
	return loadRules(ctx, q, func(a sql.NullString) bool { return !a.Valid || a.String == account })
}

// LoadAllRules is rules.load_rules(conn) without an account.
func LoadAllRules(ctx context.Context, q Querier) ([]Rule, error) {
	return loadRules(ctx, q, func(sql.NullString) bool { return true })
}

// ExportedRule is one entry of export_rules; account and dest are left out when empty.
type ExportedRule struct {
	ID      string      `json:"id"`
	Account string      `json:"account,omitempty"`
	Action  string      `json:"action"`
	Dest    string      `json:"dest,omitempty"`
	When    []Condition `json:"when"`
}

// Export is the document export_rules returns.
type Export struct {
	Rules []ExportedRule `json:"rules"`
}

// ExportRules is rules.export_rules.
func ExportRules(ctx context.Context, q Querier) (Export, error) {
	loaded, err := LoadAllRules(ctx, q)
	if err != nil {
		return Export{}, err
	}
	out := Export{Rules: make([]ExportedRule, len(loaded))}
	for i, r := range loaded {
		out.Rules[i] = ExportedRule{ID: r.ID, Action: r.Action, When: append([]Condition{}, r.Conditions...)}
		if r.Account != nil {
			out.Rules[i].Account = *r.Account
		}
		if r.Dest != nil {
			out.Rules[i].Dest = *r.Dest
		}
	}
	return out, nil
}

// Picked is a folder that cleared the learned-rule thresholds.
type Picked struct {
	Folder string
	Purity float64
	Total  int
}

// PickPure is rules.pick_pure with the configured thresholds.
func PickPure(counts map[string]int) (Picked, bool) {
	return pickPure(counts, config.RuleMinN, config.RuleMinPurity)
}

func pickPure(counts map[string]int, minN int, minPurity float64) (Picked, bool) {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total < minN {
		return Picked{}, false
	}
	folder, top := "", -1
	for f, n := range counts {
		if n > top || (n == top && f > folder) {
			folder, top = f, n
		}
	}
	purity := float64(top) / float64(total)
	if purity < minPurity {
		return Picked{}, false
	}
	return Picked{folder, purity, total}, true
}

// LearnedCounts is rules.learned_counts: filed messages of account per folder whose columns equal values,
// leaving out message excludeID when it is not 0.
func LearnedCounts(ctx context.Context, q Querier, account string, columns []string, values []string, excludeID int64) (map[string]int, error) {
	where := make([]string, len(columns))
	for i, c := range columns {
		where[i] = c + " = ?"
	}
	args := []any{account}
	for _, v := range values {
		args = append(args, v)
	}
	query := "SELECT folder, COUNT(*) AS n FROM filed_messages WHERE account = ? AND " + strings.Join(where, " AND ")
	if excludeID != 0 {
		query += " AND id != ?"
		args = append(args, excludeID)
	}
	query += " GROUP BY folder"
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var folder string
		var n int
		if err := rows.Scan(&folder, &n); err != nil {
			return nil, err
		}
		counts[folder] = n
	}
	return counts, rows.Err()
}

// Learned is a learned rule that matched a message.
type Learned struct {
	Level  string
	Dest   string
	Purity float64
	N      int
}

func levelValues(m *Message, columns []string) []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = m.scalar(c)
	}
	return out
}

// LearnedMatch is rules.learned_match; nil when no level clears the thresholds.
func LearnedMatch(ctx context.Context, q Querier, m *Message) (*Learned, error) {
	for _, level := range LearnedLevels {
		values := levelValues(m, level.Columns)
		if !allNonEmpty(values) {
			continue
		}
		counts, err := LearnedCounts(ctx, q, m.Account, level.Columns, values, m.ID)
		if err != nil {
			return nil, err
		}
		if p, ok := PickPure(counts); ok {
			return &Learned{level.Name, p.Folder, p.Purity, p.Total}, nil
		}
	}
	return nil, nil
}

func allNonEmpty(values []string) bool {
	for _, v := range values {
		if v == "" {
			return false
		}
	}
	return true
}
