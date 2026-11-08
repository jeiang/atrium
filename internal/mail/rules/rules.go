package rules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"regexp"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/py"
	"golang.org/x/text/cases"
)

const (
	AgeField      = "age_days"
	AgeOp         = "gte"
	secondsPerDay = 86400
)

type Action string

const (
	ActionFile   Action = "file"
	ActionDelete Action = "delete"
	ActionNone   Action = "none"
)

var log = logx.Logger("atrium.rules")

var (
	ListFields = map[string]string{
		"to":       "to_addrs",
		"dkim_d":   "dkim_d",
		"esp":      "esp",
		"att_type": "att_types",
		"att_name": "att_names",
	}
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
	FieldNames = []string{
		"from_addr", "from_domain", "from_name", "reply_to", "rpath_domain", "list_id", "subject", "subject_tmpl",
		"msgid_domain", "precedence", "category", "to", "dkim_d", "esp", "att_type", "att_name",
	}
	Ops          = []string{"eq", "in", "suffix", "re", "present", "absent"}
	ValuelessOps = []string{"present", "absent"}
)

type LearnedLevel struct {
	Name    string
	Columns []string
}

var LearnedLevels = []LearnedLevel{
	{"addr", []string{"from_addr"}},
	{"addr+tmpl", []string{"from_addr", "subject_tmpl"}},
	{"domain", []string{"from_domain"}},
}

type RuleError struct {
	Code string
	Msg  string
}

func (e RuleError) Error() string { return e.Msg }

func ruleErrorf(format string, args ...any) error {
	return RuleError{Code: "rule", Msg: fmt.Sprintf(format, args...)}
}

func ruleErrorCode(code, format string, args ...any) error {
	return RuleError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

type Condition struct {
	field    string
	op       string
	value    any
	hasValue bool
	matcher  *regexp.Regexp
}

func (c Condition) Field() string      { return c.field }
func (c Condition) Op() string         { return c.op }
func (c Condition) HasValue() bool     { return c.hasValue }
func (c Condition) Value() (any, bool) { return c.value, c.hasValue }

type Rule struct {
	ID         string
	Account    *string
	Action     Action
	Dest       *string
	Conditions []Condition
}

type conditionJSON struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

func (c Condition) dict() conditionJSON {
	d := conditionJSON{Field: c.field, Op: c.op}
	if c.hasValue {
		d.Value = c.value
	}
	return d
}

func (c Condition) JSON() string {
	b, _ := json.Marshal(c.dict())
	return string(b)
}

func (c Condition) MarshalJSON() ([]byte, error) { return json.Marshal(c.dict()) }

func ConditionsJSON(conds []Condition) string {
	parts := make([]string, len(conds))
	for i, c := range conds {
		parts[i] = c.JSON()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

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
	Seen        bool
	Flagged     bool
	Digest      string
	OTP6        bool
	VCode       bool
	Gone        bool
}

const MessageColumns = "id, account, from_addr, from_domain, from_name, reply_to, rpath_domain, list_id, subject, " +
	"subject_tmpl, msgid_domain, precedence, category, to_addrs, dkim_d, esp, att_types, att_names, date_ts, " +
	"in_inbox, embedded, COALESCE(folder, ''), location, seen, flagged, digest, k_otp6, k_vcode, gone_at IS NOT NULL"

type Scanner interface{ Scan(dest ...any) error }

func ScanMessage(s Scanner) (Message, error) {
	var m Message
	var dateTS sql.NullFloat64
	var inbox, embedded int64
	err := s.Scan(&m.ID, &m.Account, &m.FromAddr, &m.FromDomain, &m.FromName, &m.ReplyTo, &m.RpathDomain, &m.ListID,
		&m.Subject, &m.SubjectTmpl, &m.MsgidDomain, &m.Precedence, &m.Category, &m.ToAddrs, &m.DKIMD, &m.ESP,
		&m.AttTypes, &m.AttNames, &dateTS, &inbox, &embedded, &m.Folder, &m.Location,
		&m.Seen, &m.Flagged, &m.Digest, &m.OTP6, &m.VCode, &m.Gone)
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

func FieldValues(m *Message, field string) ([]string, error) {
	if column, ok := ListFields[field]; ok {
		var raw any
		err := json.Unmarshal([]byte(m.list(field)), &raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", column, err)
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: %w", column, errNotList)
		}
		out := make([]string, len(items))
		for i, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s: %w", column, errNotStr)
			}
			out[i] = s
		}
		return out, nil
	}
	if _, ok := ScalarFields[field]; ok {
		return []string{m.scalar(field)}, nil
	}
	return nil, fmt.Errorf("unknown field %q", field)
}

var (
	errNotList = errors.New("expected a JSON list")
	errNotStr  = errors.New("expected a list of strings")
)

func ageMatches(m *Message, days, now float64) bool {
	return m.DateTS != 0 && (now-m.DateTS)/secondsPerDay >= days
}

func domainSuffix(value, suffix string) bool {
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

func compilePattern(value string) (*regexp.Regexp, error) { return regexp.Compile("(?i)" + value) }

func (c Condition) Matches(m *Message, now float64) (bool, error) {
	if c.field == AgeField {
		days, ok := number(c.value)
		if !ok {
			return false, fmt.Errorf("age_days value %s: not a number", fmt.Sprint(c.value))
		}
		return ageMatches(m, days, now), nil
	}
	values, err := FieldValues(m, c.field)
	if err != nil {
		return false, err
	}
	switch c.op {
	case "present":
		return anyNonEmpty(values), nil
	case "absent":
		return !anyNonEmpty(values), nil
	case "eq":
		target, ok := c.value.(string)
		if !ok {
			return false, fmt.Errorf("op eq: value %s: not a string", fmt.Sprint(c.value))
		}
		target = cases.Fold().String(target)
		for _, v := range values {
			if cases.Fold().String(v) == target {
				return true, nil
			}
		}
		return false, nil
	case "in":
		list, err := members(c.value)
		if err != nil {
			return false, err
		}
		targets := map[string]bool{}
		for _, t := range list {
			targets[cases.Fold().String(t)] = true
		}
		for _, v := range values {
			if targets[cases.Fold().String(v)] {
				return true, nil
			}
		}
		return false, nil
	case "suffix":
		list, err := members(c.value)
		if err != nil {
			return false, err
		}
		targets := make([]string, len(list))
		for i, t := range list {
			targets[i] = cases.Fold().String(t)
		}
		for _, v := range values {
			if v == "" {
				continue
			}
			folded := cases.Fold().String(v)
			for _, t := range targets {
				if domainSuffix(folded, t) {
					return true, nil
				}
			}
		}
		return false, nil
	case "re":
		re := c.matcher
		if re == nil {
			return false, fmt.Errorf("op re: pattern %s was not compiled", fmt.Sprint(c.value))
		}
		for _, v := range values {
			if v == "" {
				continue
			}
			if re.MatchString(v) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("unknown op %q", c.op)
}

func anyNonEmpty(values []string) bool {
	for _, v := range values {
		if v != "" {
			return true
		}
	}
	return false
}

func (r Rule) Matches(m *Message, now float64) (bool, error) {
	for _, c := range r.Conditions {
		ok, err := c.Matches(m, now)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func FirstMatch(m *Message, rules []Rule, now float64) (*Rule, error) {
	for i := range rules {
		ok, err := rules[i].Matches(m, now)
		if err != nil {
			return nil, err
		}
		if ok {
			return &rules[i], nil
		}
	}
	return nil, nil
}

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
		if !ok || strings.TrimSpace(s) == "" {
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

func ValidateCondition(raw any) (Condition, error) {
	get, ok := object(raw)
	if !ok {
		return Condition{}, ruleErrorf("condition must be an object: %s", fmt.Sprint(raw))
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
		return Condition{field: field, op: op, value: value, hasValue: true}, nil
	}
	if !fieldIsStr || (ListFields[field] == "" && ScalarFields[field] == "") {
		return Condition{}, ruleErrorf("unknown field %s", fmt.Sprint(fieldV))
	}
	if !opIsStr || !inList(op, Ops) {
		return Condition{}, ruleErrorf("unknown op %s", fmt.Sprint(opV))
	}
	if inList(op, ValuelessOps) {
		if value != nil {
			return Condition{}, ruleErrorf("op %s takes no value", op)
		}
		return Condition{field: field, op: op}, nil
	}
	text, isStr := value.(string)
	if (op == "eq" || op == "re") && !isStr {
		return Condition{}, ruleErrorf("op %s needs a string value", op)
	}
	if isStr && strings.TrimSpace(text) == "" {
		return Condition{}, ruleErrorCode("op-"+op+"-value", "op %s needs a value", op)
	}
	list, isList := valueList(value)
	if op == "in" && !isList {
		return Condition{}, ruleErrorCode("op-in-value", "op in needs a non-empty list of strings")
	}
	if op == "suffix" && !isStr && !isList {
		return Condition{}, ruleErrorCode("op-suffix-value", "op suffix needs a string or list of strings")
	}
	if op == "re" {
		re, err := compilePattern(text)
		if err != nil {
			return Condition{}, ruleErrorf("invalid regex %s: %s", strconv.Quote(text), err)
		}
		if isList {
			value = list
		}
		return Condition{field: field, op: op, value: value, hasValue: true, matcher: re}, nil
	}
	if isList {
		value = list
	}
	return Condition{field: field, op: op, value: value, hasValue: true}, nil
}

func NewCondition(field, op string, value any) (Condition, error) {
	raw := map[string]any{"field": field, "op": op}
	if value != nil {
		raw["value"] = value
	}
	return ValidateCondition(raw)
}

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
	action := any(string(ActionFile))
	if v, found := get("action"); found {
		action = v
	}
	act, _ := action.(string)
	if Action(act) != ActionFile && Action(act) != ActionDelete {
		return Rule{}, ruleErrorf("%s: unknown action %s", id, fmt.Sprint(action))
	}
	destV, _ := get("dest")
	dest, destIsStr := destV.(string)
	if Action(act) == ActionFile && (!destIsStr || dest == "") {
		return Rule{}, ruleErrorCode("file-dest", "%s: file rule needs dest", id)
	}
	if Action(act) == ActionDelete && destV != nil {
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
		return Rule{}, ruleErrorCode("when-empty", "%s: when must be a non-empty list", id)
	}
	rule := Rule{ID: id, Action: Action(act)}
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
				i, r.ID, nullable(r.Account), string(r.Action), nullable(r.Dest), ConditionsJSON(r.Conditions)); err != nil {
				return err
			}
		}
		return nil
	})
	return len(parsed), err
}

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type MessageInsert struct {
	Account      string
	MailboxID    int64
	UIDValidity  int64
	UID          uint32
	MsgID        string
	Digest       string
	GmMsgID      string
	GmThrID      string
	InternalDate string
	DateTS       float64
	Size         int64
	RawPath      string
	Flags        string
	Seen         int
	Flagged      int
	Labels       string
	InInbox      int
	Sent         int
	Location     string
	Folder       *string
	FromAddr     string
	FromDomain   string
	FromName     string
	ToAddrs      string
	ReplyTo      string
	RPathDomain  string
	ListID       string
	Precedence   string
	Subject      string
	SubjectTmpl  string
	MsgIDDomain  string
	InReplyTo    string
	Refs         string
	DKIMD        string
	ESP          string
	AttTypes     string
	AttNames     string
	KOrder       bool
	KVCode       bool
	KOTP6        bool
	FirstSeen    float64
}

var messageInsertColumns = []struct {
	name  string
	value func(*MessageInsert) any
}{
	{"account", func(m *MessageInsert) any { return m.Account }},
	{"mailbox_id", func(m *MessageInsert) any { return m.MailboxID }},
	{"uidvalidity", func(m *MessageInsert) any { return m.UIDValidity }},
	{"uid", func(m *MessageInsert) any { return m.UID }},
	{"msgid", func(m *MessageInsert) any { return m.MsgID }},
	{"digest", func(m *MessageInsert) any { return m.Digest }},
	{"gm_msgid", func(m *MessageInsert) any { return nullableText(m.GmMsgID) }},
	{"gm_thrid", func(m *MessageInsert) any { return nullableText(m.GmThrID) }},
	{"internaldate", func(m *MessageInsert) any { return m.InternalDate }},
	{"date_ts", func(m *MessageInsert) any { return m.DateTS }},
	{"size", func(m *MessageInsert) any { return m.Size }},
	{"raw_path", func(m *MessageInsert) any { return m.RawPath }},
	{"flags", func(m *MessageInsert) any { return m.Flags }},
	{"seen", func(m *MessageInsert) any { return m.Seen }},
	{"flagged", func(m *MessageInsert) any { return m.Flagged }},
	{"labels", func(m *MessageInsert) any { return m.Labels }},
	{"in_inbox", func(m *MessageInsert) any { return m.InInbox }},
	{"sent", func(m *MessageInsert) any { return m.Sent }},
	{"location", func(m *MessageInsert) any { return m.Location }},
	{"folder", func(m *MessageInsert) any { return m.Folder }},
	{"from_addr", func(m *MessageInsert) any { return m.FromAddr }},
	{"from_domain", func(m *MessageInsert) any { return m.FromDomain }},
	{"from_name", func(m *MessageInsert) any { return m.FromName }},
	{"to_addrs", func(m *MessageInsert) any { return m.ToAddrs }},
	{"reply_to", func(m *MessageInsert) any { return m.ReplyTo }},
	{"rpath_domain", func(m *MessageInsert) any { return m.RPathDomain }},
	{"list_id", func(m *MessageInsert) any { return m.ListID }},
	{"precedence", func(m *MessageInsert) any { return m.Precedence }},
	{"subject", func(m *MessageInsert) any { return m.Subject }},
	{"subject_tmpl", func(m *MessageInsert) any { return m.SubjectTmpl }},
	{"msgid_domain", func(m *MessageInsert) any { return m.MsgIDDomain }},
	{"in_reply_to", func(m *MessageInsert) any { return m.InReplyTo }},
	{"refs", func(m *MessageInsert) any { return m.Refs }},
	{"dkim_d", func(m *MessageInsert) any { return m.DKIMD }},
	{"esp", func(m *MessageInsert) any { return m.ESP }},
	{"att_types", func(m *MessageInsert) any { return m.AttTypes }},
	{"att_names", func(m *MessageInsert) any { return m.AttNames }},
	{"k_order", func(m *MessageInsert) any { return bit(m.KOrder) }},
	{"k_vcode", func(m *MessageInsert) any { return bit(m.KVCode) }},
	{"k_otp6", func(m *MessageInsert) any { return bit(m.KOTP6) }},
	{"first_seen", func(m *MessageInsert) any { return m.FirstSeen }},
}

func bit(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func InsertMessage(ctx context.Context, q Execer, m MessageInsert) (int64, error) {
	names := make([]string, len(messageInsertColumns))
	marks := make([]string, len(messageInsertColumns))
	args := make([]any, len(messageInsertColumns))
	for i, c := range messageInsertColumns {
		names[i] = c.name
		marks[i] = "?"
		args[i] = c.value(&m)
	}
	res, err := q.ExecContext(ctx, "INSERT INTO messages ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type Querier = db.Querier

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
		r := Rule{ID: id, Action: Action(action)}
		if account.Valid {
			r.Account = &account.String
		}
		if dest.Valid {
			r.Dest = &dest.String
		}
		conditions, err := parseStored(conds)
		if err != nil {
			log.Warn("skipping rule with invalid conditions", "rule", id, "error", err)
			continue
		}
		r.Conditions = conditions
		out = append(out, r)
	}
	return out, rows.Err()
}

type StoredRule struct {
	ID         string
	Account    *string
	Action     string
	Dest       *string
	Conditions string
}

func ValidateStored(row StoredRule) error {
	if row.ID == "" {
		return ruleErrorf("rule needs a string id")
	}
	if Action(row.Action) != ActionFile && Action(row.Action) != ActionDelete {
		return ruleErrorf("%s: unknown action %q", row.ID, row.Action)
	}
	if Action(row.Action) == ActionFile && (row.Dest == nil || *row.Dest == "") {
		return ruleErrorf("%s: file rule needs dest", row.ID)
	}
	if Action(row.Action) == ActionDelete && row.Dest != nil {
		return ruleErrorf("%s: delete rule takes no dest", row.ID)
	}
	if _, err := parseStored(row.Conditions); err != nil {
		return ruleErrorf("%s: %s", row.ID, err)
	}
	return nil
}

func parseStored(text string) ([]Condition, error) {
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, err
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errNotList
	}
	out := make([]Condition, len(items))
	for i, item := range items {
		cond, err := ValidateCondition(item)
		if err != nil {
			return nil, fmt.Errorf("condition %d: %w", i, err)
		}
		out[i] = cond
	}
	return out, nil
}

func LoadRules(ctx context.Context, q Querier, account string) ([]Rule, error) {
	return loadRules(ctx, q, func(a sql.NullString) bool { return !a.Valid || a.String == account })
}

func LoadAllRules(ctx context.Context, q Querier) ([]Rule, error) {
	return loadRules(ctx, q, func(sql.NullString) bool { return true })
}

type exportedRule struct {
	ID      string          `json:"id"`
	Account string          `json:"account,omitempty"`
	Action  string          `json:"action"`
	Dest    string          `json:"dest,omitempty"`
	When    []conditionJSON `json:"when"`
}

type exportedRules struct {
	Rules []exportedRule `json:"rules"`
}

func ExportRules(ctx context.Context, q Querier) (exportedRules, error) {
	loaded, err := LoadAllRules(ctx, q)
	if err != nil {
		return exportedRules{}, err
	}
	out := make([]exportedRule, len(loaded))
	for i, r := range loaded {
		e := exportedRule{ID: r.ID, Action: string(r.Action)}
		if r.Account != nil {
			e.Account = *r.Account
		}
		if r.Dest != nil {
			e.Dest = *r.Dest
		}
		e.When = make([]conditionJSON, len(r.Conditions))
		for j, c := range r.Conditions {
			e.When[j] = c.dict()
		}
		out[i] = e
	}
	return exportedRules{Rules: out}, nil
}

type Picked struct {
	Folder string
	Purity float64
	Total  int
}

type Thresholds struct {
	MinN      int
	MinPurity float64
}

func ReadThresholds(ctx context.Context, q Querier) (Thresholds, error) {
	r := endpoint.Read(ctx, q)
	t := Thresholds{MinN: r.Int(mail.KeyRuleMinN), MinPurity: r.Float(mail.KeyRuleMinPurity)}
	return t, r.Err()
}

func PickPure(counts map[string]int, t Thresholds) (Picked, bool) {
	minN, minPurity := t.MinN, t.MinPurity
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

type LearnedGroup struct {
	Level  string
	Key    string
	Counts map[string]int
}

func LearnedGroups(ctx context.Context, q Querier, account string) ([]LearnedGroup, error) {
	var out []LearnedGroup
	for _, level := range LearnedLevels {
		nonblank := make([]string, len(level.Columns))
		for i, c := range level.Columns {
			nonblank[i] = c + " != ''"
		}
		selectCols := strings.Join(level.Columns, ", ")
		rows, err := q.QueryContext(ctx,
			"SELECT "+selectCols+", folder, COUNT(*) AS n FROM filed_messages WHERE account = ? AND "+
				strings.Join(nonblank, " AND ")+" GROUP BY "+selectCols+", folder", account)
		if err != nil {
			return nil, err
		}
		seen := map[string]*LearnedGroup{}
		var order []string
		err = func() error {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				parts := make([]string, len(level.Columns))
				var folder string
				var n int
				dest := make([]any, 0, len(parts)+2)
				for i := range parts {
					dest = append(dest, &parts[i])
				}
				if err := rows.Scan(append(dest, &folder, &n)...); err != nil {
					return err
				}
				key := strings.Join(parts, " | ")
				g := seen[key]
				if g == nil {
					g = &LearnedGroup{Level: level.Name, Key: key, Counts: map[string]int{}}
					seen[key] = g
					order = append(order, key)
				}
				g.Counts[folder] = n
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
		for _, key := range order {
			out = append(out, *seen[key])
		}
	}
	return out, nil
}

func learnedCounts(ctx context.Context, q Querier, account string, columns []string, values []string, excludeID int64) (map[string]int, error) {
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

func LearnedMatch(ctx context.Context, q Querier, m *Message, t Thresholds) (*Learned, error) {
	for _, level := range LearnedLevels {
		values := levelValues(m, level.Columns)
		if !allNonEmpty(values) {
			continue
		}
		counts, err := learnedCounts(ctx, q, m.Account, level.Columns, values, m.ID)
		if err != nil {
			return nil, err
		}
		if p, ok := PickPure(counts, t); ok {
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
