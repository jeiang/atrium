// Package ruleedit is src/atrium/ruleedit.py: the rule editor behind the
// rules page and the agent's rule tools.
package ruleedit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/py"
)

const (
	PreviewID    = "preview"
	DefaultLimit = 20
)

type featureField struct{ field, op string }

var featureFields = []featureField{
	{"from_addr", "eq"},
	{"from_domain", "suffix"},
	{"list_id", "eq"},
	{"subject_tmpl", "eq"},
	{"reply_to", "eq"},
	{"rpath_domain", "suffix"},
	{"msgid_domain", "suffix"},
	{"precedence", "eq"},
	{"category", "eq"},
}

var listFeatures = []featureField{{"dkim_d", "in"}, {"esp", "in"}}

func ruleErrorf(msg string) error { return rules.RuleError(msg) }

// withID is {**raw, "id": id}.
func withID(raw any, id string) (any, bool) {
	switch raw := raw.(type) {
	case py.Dict:
		return raw.Set("id", id), true
	case map[string]any:
		out := make(map[string]any, len(raw)+1)
		for k, v := range raw {
			out[k] = v
		}
		out["id"] = id
		return out, true
	}
	return nil, false
}

func get(raw any, key string) any {
	switch raw := raw.(type) {
	case py.Dict:
		v, _ := raw.Get(key)
		return v
	case map[string]any:
		return raw[key]
	}
	return nil
}

func checked(raw any, id string) (rules.Rule, error) {
	doc, ok := withID(raw, id)
	if !ok {
		return rules.Rule{}, fmt.Errorf("%w: 'raw' object is not a mapping", httpx.ErrType)
	}
	rule, err := rules.ValidateRule(doc)
	if err != nil {
		return rules.Rule{}, err
	}
	if rule.Account != nil {
		if _, known := config.Account(*rule.Account); !known {
			return rules.Rule{}, ruleErrorf("unknown account " + py.Repr(*rule.Account))
		}
	}
	return rule, nil
}

func store(r rules.Rule) []any {
	var account, dest any
	if r.Account != nil {
		account = *r.Account
	}
	if r.Dest != nil {
		dest = *r.Dest
	}
	return []any{account, r.Action, dest, rules.ConditionsJSON(r.Conditions)}
}

// RuleDict is rule_dict: the rule as the editor shows it.
type RuleDict struct {
	ID      string            `json:"id"`
	Account *string           `json:"account"`
	Action  string            `json:"action"`
	Dest    *string           `json:"dest"`
	When    []rules.Condition `json:"when"`
}

func ruleDict(r rules.Rule) RuleDict {
	return RuleDict{r.ID, r.Account, r.Action, r.Dest, slices.Clone(r.Conditions)}
}

// ListRules is list_rules: the operator rules in effect for account.
func ListRules(ctx context.Context, q rules.Querier, account string) ([]RuleDict, error) {
	loaded, err := rules.LoadRules(ctx, q, account)
	return ruleDicts(loaded, err)
}

// ListAllRules is list_rules(conn, None): every operator rule.
func ListAllRules(ctx context.Context, q rules.Querier) ([]RuleDict, error) {
	loaded, err := rules.LoadAllRules(ctx, q)
	return ruleDicts(loaded, err)
}

func ruleDicts(loaded []rules.Rule, err error) ([]RuleDict, error) {
	if err != nil {
		return nil, err
	}
	out := make([]RuleDict, len(loaded))
	for i, r := range loaded {
		out[i] = ruleDict(r)
	}
	return out, nil
}

// CreateRule is create_rule: it validates raw, generates an id when raw has none and appends the rule.
func CreateRule(ctx context.Context, d *db.DB, raw any) (rules.Rule, error) {
	id := "r-" + randomHex()
	if py.Truthy(get(raw, "id")) {
		s, ok := get(raw, "id").(string)
		if !ok {
			return rules.Rule{}, ruleErrorf("rule needs a string id")
		}
		id = s
	}
	rule, err := checked(raw, id)
	if err != nil {
		return rules.Rule{}, err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, "SELECT 1 FROM rules WHERE id = ?", rule.ID).Scan(&one); {
		case err == nil:
			return ruleErrorf("rule " + py.Repr(rule.ID) + " already exists")
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var position int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position) + 1, 0) FROM rules").Scan(&position); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (?, ?, ?, ?, ?, ?)",
			slices.Concat([]any{position, rule.ID}, store(rule))...)
		return err
	})
	return rule, err
}

func randomHex() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// UpdateRule is update_rule.
func UpdateRule(ctx context.Context, d *db.DB, id string, raw any) (rules.Rule, error) {
	rule, err := checked(raw, id)
	if err != nil {
		return rules.Rule{}, err
	}
	res, err := d.Write.ExecContext(ctx,
		"UPDATE rules SET account = ?, action = ?, dest = ?, conditions = ? WHERE id = ?", append(store(rule), id)...)
	if err != nil {
		return rules.Rule{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return rules.Rule{}, ruleErrorf("unknown rule " + py.Repr(id))
	}
	return rule, nil
}

// DeleteRule is delete_rule: whether a rule was removed.
func DeleteRule(ctx context.Context, d *db.DB, id string) (bool, error) {
	res, err := d.Write.ExecContext(ctx, "DELETE FROM rules WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// ReorderRules is reorder_rules: the listed rules take the positions they already held, in the new order.
func ReorderRules(ctx context.Context, d *db.DB, ordered []string) error {
	seen := map[string]bool{}
	for _, id := range ordered {
		seen[id] = true
	}
	if len(seen) != len(ordered) {
		return ruleErrorf("duplicate rule ids")
	}
	args := make([]any, len(ordered))
	for i, id := range ordered {
		args[i] = id
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT position FROM rules WHERE id IN ("+marks(len(ordered))+")", args...)
		if err != nil {
			return err
		}
		slots, err := db.Collect(rows, func(r *sql.Rows) (int, error) {
			var p int
			err := r.Scan(&p)
			return p, err
		})
		if err != nil {
			return err
		}
		if len(slots) != len(ordered) {
			return ruleErrorf("unknown rule id in order")
		}
		slices.Sort(slots)
		for i, id := range ordered {
			if _, err := tx.ExecContext(ctx, "UPDATE rules SET position = ? WHERE id = ?", slots[i], id); err != nil {
				return err
			}
		}
		return nil
	})
}

// PreviewSample is one matching message in a preview.
type PreviewSample struct {
	ID       int64   `json:"id"`
	Account  string  `json:"account"`
	Subject  string  `json:"subject"`
	FromAddr string  `json:"from_addr"`
	FromName string  `json:"from_name"`
	Folder   string  `json:"folder"`
	DateTS   float64 `json:"date_ts"`
}

// Preview is what preview_rule returns.
type Preview struct {
	Total   int             `json:"total"`
	Count   int             `json:"count"`
	InInbox int             `json:"in_inbox"`
	Sample  []PreviewSample `json:"sample"`
}

// PreviewRule is preview_rule: how many live messages the rule would match.
func PreviewRule(ctx context.Context, q rules.Querier, raw any, limit int) (Preview, error) {
	id := PreviewID
	if py.Truthy(get(raw, "id")) {
		s, ok := get(raw, "id").(string)
		if !ok {
			return Preview{}, ruleErrorf("rule needs a string id")
		}
		id = s
	}
	rule, err := checked(raw, id)
	if err != nil {
		return Preview{}, err
	}
	query := "SELECT " + rules.MessageColumns + " FROM messages WHERE gone_at IS NULL AND sent = 0"
	var args []any
	if rule.Account != nil && *rule.Account != "" {
		query += " AND account = ?"
		args = append(args, *rule.Account)
	}
	query += " ORDER BY date_ts DESC, id DESC"
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return Preview{}, err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (rules.Message, error) { return rules.ScanMessage(r) })
	if err != nil {
		return Preview{}, err
	}
	out := Preview{Total: len(all), Sample: []PreviewSample{}}
	for i := range all {
		m := &all[i]
		ok, err := rule.Matches(m)
		if err != nil {
			return Preview{}, err
		}
		if !ok {
			continue
		}
		out.Count++
		if m.InInbox {
			out.InInbox++
		}
		if len(out.Sample) < limit {
			folder := m.Folder
			if folder == "" {
				folder = m.Location
			}
			out.Sample = append(out.Sample, PreviewSample{m.ID, m.Account, m.Subject, m.FromAddr, m.FromName, folder, m.DateTS})
		}
	}
	return out, nil
}

// Features is what message_features returns: conditions that describe a message, for pre-filling a rule.
type Features struct {
	Account    string            `json:"account"`
	Dest       *string           `json:"dest"`
	Conditions []rules.Condition `json:"conditions"`
	Default    []rules.Condition `json:"default"`
}

// MessageFeatures is message_features. An unknown message is a RuleError.
func MessageFeatures(ctx context.Context, q rules.Querier, messageID int64) (Features, error) {
	row := q.QueryRowContext(ctx, "SELECT "+rules.MessageColumns+" FROM messages WHERE id = ?", messageID)
	m, err := rules.ScanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Features{}, ruleErrorf("unknown message " + strconv.FormatInt(messageID, 10))
	}
	if err != nil {
		return Features{}, err
	}
	found := []rules.Condition{}
	for _, f := range featureFields {
		if v := scalarOf(&m, f.field); v != "" {
			found = append(found, rules.Condition{Field: f.field, Op: f.op, Value: v, HasValue: true})
		}
	}
	for _, f := range listFeatures {
		values, err := rules.FieldValues(&m, f.field)
		if err != nil {
			return Features{}, err
		}
		if len(values) > 0 {
			found = append(found, rules.Condition{Field: f.field, Op: f.op, Value: values, HasValue: true})
		}
	}
	var dest sql.NullString
	err = q.QueryRowContext(ctx,
		"SELECT dest FROM decisions WHERE message_id = ? AND dest IS NOT NULL ORDER BY id DESC LIMIT 1", messageID).Scan(&dest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Features{}, err
	}
	out := Features{Account: m.Account, Conditions: found, Default: []rules.Condition{}}
	if dest.Valid {
		out.Dest = &dest.String
	}
	for _, c := range found {
		if c.Field == "from_addr" {
			out.Default = append(out.Default, c)
			break
		}
	}
	return out, nil
}

func scalarOf(m *rules.Message, field string) string {
	values, _ := rules.FieldValues(m, field)
	return values[0]
}

// LearnedRule is one learned rule in the summary.
type LearnedRule struct {
	Level  string  `json:"level"`
	Key    string  `json:"key"`
	Dest   string  `json:"dest"`
	Purity float64 `json:"purity"`
	N      int     `json:"n"`
}

// DefaultLearnedLimit is learned_rules_summary's default limit.
const DefaultLearnedLimit = 50

// LearnedRulesSummary is learned_rules_summary: the learned rules of an account, most-supported first.
func LearnedRulesSummary(ctx context.Context, q rules.Querier, account string, limit int) ([]LearnedRule, error) {
	found := []LearnedRule{}
	for _, level := range rules.LearnedLevels {
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
		type group struct {
			key    string
			counts map[string]int
		}
		var order [][2]string
		groups := map[[2]string]*group{}
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
				var tuple [2]string
				copy(tuple[:], parts)
				g := groups[tuple]
				if g == nil {
					g = &group{strings.Join(parts, " | "), map[string]int{}}
					groups[tuple] = g
					order = append(order, tuple)
				}
				g.counts[folder] = n
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
		for _, k := range order {
			g := groups[k]
			if p, ok := rules.PickPure(g.counts); ok {
				found = append(found, LearnedRule{level.Name, g.key, p.Folder, p.Purity, p.Total})
			}
		}
	}
	slices.SortStableFunc(found, func(a, b LearnedRule) int {
		switch {
		case a.N != b.N:
			return b.N - a.N
		case a.Purity != b.Purity:
			if a.Purity > b.Purity {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.Level, b.Level); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return found[:min(limit, len(found))], nil
}
