package rules_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func row(edit func(*rules.Message)) *rules.Message {
	m := &rules.Message{
		FromAddr: "a@b.test", FromDomain: "b.test",
		ToAddrs: "[]", DKIMD: "[]", ESP: "[]", AttTypes: "[]", AttNames: "[]",
	}
	if edit != nil {
		edit(m)
	}
	return m
}

func cond(field, op string, value ...any) py.Dict {
	d := py.Dict{{Key: "field", Value: field}, {Key: "op", Value: op}}
	if len(value) > 0 {
		d = append(d, py.Pair{Key: "value", Value: value[0]})
	}
	return d
}

func rule(id, dest string, when ...any) py.Dict {
	d := py.Dict{{Key: "id", Value: id}}
	if dest != "" {
		d = append(d, py.Pair{Key: "dest", Value: dest})
	}
	return append(d, py.Pair{Key: "when", Value: when})
}

func validate(t *testing.T, c py.Dict) rules.Condition {
	t.Helper()
	got, err := rules.ValidateCondition(c)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConditionOperators(t *testing.T) {
	for _, c := range []struct {
		name string
		cond py.Dict
		edit func(*rules.Message)
		want bool
	}{
		{"eq casefolded", cond("from_addr", "eq", "A@B.TEST"), nil, true},
		{"eq other", cond("from_addr", "eq", "x@b.test"), nil, false},
		{"in", cond("from_domain", "in", []any{"x.test", "B.test"}), nil, true},
		{"suffix subdomain", cond("from_domain", "suffix", "b.test"), func(m *rules.Message) { m.FromDomain = "mail.b.test" }, true},
		{"suffix not a domain boundary", cond("from_domain", "suffix", "b.test"), func(m *rules.Message) { m.FromDomain = "notb.test" }, false},
		{"suffix list", cond("from_domain", "suffix", []any{"q.test", "b.test"}), nil, true},
		{"re word", cond("subject", "re", `\balert\b`), func(m *rules.Message) { m.Subject = "Security ALERT now" }, true},
		{"re anchored", cond("subject", "re", `^alert`), func(m *rules.Message) { m.Subject = "an alert" }, false},
		{"present", cond("list_id", "present"), func(m *rules.Message) { m.ListID = "<x>" }, true},
		{"present empty", cond("list_id", "present"), nil, false},
		{"absent", cond("list_id", "absent"), nil, true},
		{"to list", cond("to", "eq", "me@x.test"), func(m *rules.Message) { m.ToAddrs = `["other@x.test", "me@x.test"]` }, true},
		{"att_type in", cond("att_type", "in", []any{"application/pdf"}), func(m *rules.Message) { m.AttTypes = `["application/pdf"]` }, true},
		{"category", cond("category", "eq", "updates"), func(m *rules.Message) { m.Category = "updates" }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := validate(t, c.cond).Matches(row(c.edit))
			if err != nil || got != c.want {
				t.Errorf("Matches = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

func TestAndWithinRuleAndFirstMatchWins(t *testing.T) {
	set, err := rules.ParseRuleset([]any{
		rule("one", "A", cond("from_domain", "eq", "b.test"), cond("subject", "re", "bill")),
		rule("two", "B", cond("from_domain", "eq", "b.test")),
		py.Dict{{Key: "id", Value: "three"}, {Key: "action", Value: "delete"}, {Key: "when", Value: []any{cond("from_domain", "eq", "b.test")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		edit func(*rules.Message)
		want string
	}{
		{func(m *rules.Message) { m.Subject = "your bill" }, "A"},
		{func(m *rules.Message) { m.Subject = "hello" }, "B"},
		{func(m *rules.Message) { m.FromDomain = "zzz.test" }, ""},
	} {
		got, err := rules.FirstMatch(row(c.edit), set)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case c.want == "" && got != nil:
			t.Errorf("matched %s, want none", got.ID)
		case c.want != "" && (got == nil || got.Dest == nil || *got.Dest != c.want):
			t.Errorf("matched %+v, want dest %s", got, c.want)
		}
	}
}

func TestInvalidRulesRejected(t *testing.T) {
	file := func(extra ...py.Pair) py.Dict { return append(py.Dict{{Key: "id", Value: "x"}}, extra...) }
	dest := py.Pair{Key: "dest", Value: "A"}
	when := func(c ...any) py.Pair { return py.Pair{Key: "when", Value: c} }
	for name, c := range map[string]struct {
		rule py.Dict
		want string
	}{
		"no dest":           {file(when(cond("from_addr", "eq", "a"))), "x: file rule needs dest"},
		"empty when":        {file(dest, when()), "x: when must be a non-empty list"},
		"unknown field":     {file(dest, when(cond("nope", "eq", "a"))), "unknown field 'nope'"},
		"unknown op":        {file(dest, when(cond("from_addr", "like", "a"))), "unknown op 'like'"},
		"bad regex":         {file(dest, when(cond("subject", "re", "("))), "invalid regex '(': missing ), unterminated subpattern at position 0"},
		"delete with dest":  {file(py.Pair{Key: "action", Value: "delete"}, dest, when(cond("from_addr", "eq", "a"))), "x: delete rule takes no dest"},
		"unknown action":    {file(py.Pair{Key: "action", Value: "nuke"}, when(cond("from_addr", "eq", "a"))), "x: unknown action 'nuke'"},
		"valueless value":   {file(dest, when(cond("from_addr", "present", "v"))), "op present takes no value"},
		"blank eq":          {file(dest, when(cond("from_addr", "eq", "  "))), "op eq needs a value"},
		"empty re":          {file(dest, when(cond("subject", "re", ""))), "op re needs a value"},
		"empty suffix":      {file(dest, when(cond("from_domain", "suffix", ""))), "op suffix needs a value"},
		"blank in member":   {file(dest, when(cond("dkim_d", "in", []any{"a.test", " "}))), "op in needs a non-empty list of strings"},
		"eq number":         {file(dest, when(cond("from_addr", "eq", int64(3)))), "op eq needs a string value"},
		"python only regex": {file(dest, when(cond("subject", "re", `\p{L}`))), `invalid regex '\\p{L}': bad escape \p at position 0`},
		"account type":      {file(dest, py.Pair{Key: "account", Value: int64(1)}, when(cond("from_addr", "eq", "a"))), "x: account must be a string"},
		"condition type":    {file(dest, when("x")), "condition must be an object: 'x'"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rules.ParseRuleset([]any{c.rule})
			var re rules.RuleError
			if !errors.As(err, &re) || string(re) != c.want {
				t.Errorf("error %v, want RuleError %q", err, c.want)
			}
		})
	}
	for _, doc := range []any{int64(3), py.Dict{{Key: "rules", Value: "x"}}} {
		_, err := rules.ParseRuleset(doc)
		if err == nil || err.Error() != `expected a list of rules or {"rules": [...]}` {
			t.Errorf("document %v: %v", doc, err)
		}
	}
	for _, bad := range []any{"x", py.Dict{{Key: "id", Value: ""}}, py.Dict{{Key: "id", Value: int64(1)}}} {
		if _, err := rules.ValidateRule(bad); err == nil {
			t.Errorf("rule %v accepted", bad)
		}
	}
}

func TestDuplicateIDsRejected(t *testing.T) {
	one := rule("x", "A", cond("from_addr", "eq", "a"))
	_, err := rules.ParseRuleset([]any{one, one})
	if err == nil || err.Error() != "duplicate rule ids" {
		t.Errorf("error %v", err)
	}
}

func TestImportExportRoundtripKeepsOrderAndScope(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	text := `{"rules": [
		{"id": "b", "account": "gmail", "action": "file", "dest": "X", "when": [{"field": "from_addr", "op": "eq", "value": "a"}]},
		{"id": "a", "action": "delete", "when": [{"field": "list_id", "op": "present"}]}
	]}`
	doc, err := py.JSONLoads([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := rules.ImportRules(ctx, d, doc); err != nil || n != 2 {
		t.Fatalf("ImportRules = %d, %v", n, err)
	}
	exported, err := rules.ExportRules(ctx, d.Read)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(exported)
	var want, have any
	_ = json.Unmarshal([]byte(text), &want)
	_ = json.Unmarshal(got, &have)
	if !jsonEqual(want, have) {
		t.Errorf("export %s", got)
	}
	ids := func(account string) string {
		loaded, err := rules.LoadRules(ctx, d.Read, account)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range loaded {
			out = append(out, r.ID)
		}
		return strings.Join(out, ",")
	}
	if ids("icloud") != "a" || ids("gmail") != "b,a" {
		t.Errorf("scopes icloud=%q gmail=%q", ids("icloud"), ids("gmail"))
	}
	bad, _ := py.JSONLoads([]byte(`{"rules": [{"id": "bad"}]}`))
	if _, err := rules.ImportRules(ctx, d, bad); err == nil {
		t.Fatal("bad import accepted")
	}
	if all, _ := rules.LoadAllRules(ctx, d.Read); len(all) != 2 {
		t.Errorf("failed import changed the table: %d rules", len(all))
	}
	var stored string
	if err := d.Read.QueryRowContext(ctx, "SELECT conditions FROM rules WHERE id = 'b'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `[{"field": "from_addr", "op": "eq", "value": "a"}]` {
		t.Errorf("stored conditions %s", stored)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestPickPureThresholds(t *testing.T) {
	for _, c := range []struct {
		counts map[string]int
		want   rules.Picked
		ok     bool
	}{
		{map[string]int{"A": 9, "B": 1}, rules.Picked{Folder: "A", Purity: 0.9, Total: 10}, true},
		{map[string]int{"A": 8, "B": 2}, rules.Picked{}, false},
		{map[string]int{"A": 2}, rules.Picked{}, false},
		{map[string]int{"A": 3}, rules.Picked{Folder: "A", Purity: 1.0, Total: 3}, true},
	} {
		got, ok := rules.PickPure(c.counts)
		if ok != c.ok || got != c.want {
			t.Errorf("PickPure(%v) = %v, %v", c.counts, got, ok)
		}
	}
}

func TestLearnedLevelsAddressThenTemplateThenDomain(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	for i := range 3 {
		rulestest.Filed(t, d, i, "Pure", "pure@one.test", "one.test", nil, nil)
	}
	for i := range 3 {
		rulestest.Filed(t, d, 10+i, "Alerts", "mixed@two.test", "two.test", nil, rulestest.Fields{"subject_tmpl": "alert <n>"})
	}
	for i := range 3 {
		rulestest.Filed(t, d, 20+i, "Bills", "mixed@two.test", "two.test", nil, rulestest.Fields{"subject_tmpl": "bill"})
	}
	for i, a := range []string{"p@three.test", "q@three.test", "r@three.test"} {
		rulestest.Filed(t, d, 30+i, "Dom", a, "three.test", nil, nil)
	}
	q1 := rulestest.Inbox(t, d, 100, "pure@one.test", "one.test", nil, nil)
	q2 := rulestest.Inbox(t, d, 101, "mixed@two.test", "two.test", nil, rulestest.Fields{"subject_tmpl": "bill"})
	q3 := rulestest.Inbox(t, d, 102, "new@three.test", "three.test", nil, nil)
	q4 := rulestest.Inbox(t, d, 103, "x@nowhere.test", "nowhere.test", nil, nil)
	get := func(id int64) *rules.Learned {
		m, err := rules.ScanMessage(d.Read.QueryRowContext(ctx, "SELECT "+rules.MessageColumns+" FROM messages WHERE id = ?", id))
		if err != nil {
			t.Fatal(err)
		}
		l, err := rules.LearnedMatch(ctx, d.Read, &m)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	for _, c := range []struct {
		id          int64
		level, dest string
	}{{q1, "addr", "Pure"}, {q2, "addr+tmpl", "Bills"}, {q3, "domain", "Dom"}, {q4, "", ""}} {
		l := get(c.id)
		switch {
		case c.level == "" && l != nil:
			t.Errorf("message %d learned %+v", c.id, l)
		case c.level != "" && (l == nil || l.Level != c.level || l.Dest != c.dest):
			t.Errorf("message %d learned %+v, want %s %s", c.id, l, c.level, c.dest)
		}
	}
}

func TestLearnedExcludesInboxGoneAndUnfiled(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	same := func(extra rulestest.Fields) rulestest.Fields {
		f := rulestest.Fields{"from_addr": "s@s.test", "from_domain": "s.test"}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	for i := range 3 {
		rulestest.Add(t, d, i, nil, "", same(rulestest.Fields{"folder": "X", "in_inbox": 1}))
	}
	for i := 3; i < 6; i++ {
		rulestest.Add(t, d, i, nil, "", same(nil))
	}
	for i := 6; i < 9; i++ {
		rulestest.Add(t, d, i, nil, "", same(rulestest.Fields{"folder": "X", "gone_at": 1.0}))
	}
	q := rulestest.Inbox(t, d, 50, "s@s.test", "s.test", nil, nil)
	m, err := rules.ScanMessage(d.Read.QueryRowContext(ctx, "SELECT "+rules.MessageColumns+" FROM messages WHERE id = ?", q))
	if err != nil {
		t.Fatal(err)
	}
	if l, err := rules.LearnedMatch(ctx, d.Read, &m); err != nil || l != nil {
		t.Errorf("LearnedMatch = %+v, %v", l, err)
	}
}

func TestAgeDaysCondition(t *testing.T) {
	at := func(ts float64) *rules.Message { return row(func(m *rules.Message) { m.DateTS = ts }) }
	nowTS := float64(time.Now().UnixNano()) / 1e9
	c := validate(t, cond("age_days", "gte", int64(7)))
	for _, tc := range []struct {
		name string
		m    *rules.Message
		want bool
	}{{"old", at(nowTS - 10*86400), true}, {"new", at(nowTS), false}, {"unknown", at(0), false}} {
		if got, err := c.Matches(tc.m); err != nil || got != tc.want {
			t.Errorf("%s: %v, %v", tc.name, got, err)
		}
	}
	if c.JSON() != `{"field": "age_days", "op": "gte", "value": 7}` {
		t.Errorf("JSON %s", c.JSON())
	}
	if f := validate(t, cond("age_days", "gte", 7.5)); f.JSON() != `{"field": "age_days", "op": "gte", "value": 7.5}` {
		t.Errorf("float JSON %s", f.JSON())
	}
	for _, bad := range []py.Dict{
		cond("age_days", "eq", int64(7)), cond("age_days", "gte", int64(0)), cond("age_days", "gte", "7"), cond("age_days", "gte", true),
	} {
		_, err := rules.ValidateCondition(bad)
		if err == nil || err.Error() != "age_days needs op gte and a positive number of days" {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestRegexCorpus(t *testing.T) {
	c := rxtest.Load(t)
	match, validation := map[string]*rx.Regexp{}, map[string]*rx.Regexp{}
	for _, p := range c.Patterns {
		var compile func(string) (*rx.Regexp, error)
		var into map[string]*rx.Regexp
		switch {
		case strings.HasPrefix(p.ID, "src/atrium/rules.py:condition_matches:"):
			compile, into = rules.Pattern, match
		case strings.HasPrefix(p.ID, "src/atrium/rules.py:validate_condition:"):
			compile, into = rules.CheckPattern, validation
		default:
			continue
		}
		re, err := compile(p.Pattern)
		if p.Error != "" {
			if err == nil || err.Error() != p.Error {
				t.Errorf("%s: %q: error %v, Python %q", p.ID, p.Pattern, err, p.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %q: %v", p.ID, p.Pattern, err)
			continue
		}
		into[p.ID] = re
	}
	if len(match) != 20 || len(validation) != 20 {
		t.Fatalf("registered %d matching and %d validation patterns", len(match), len(validation))
	}
	rxtest.Run(t, match)
	rxtest.Run(t, validation)
}

func TestSpecialFloatsRoundTrip(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	doc, err := py.JSONLoads([]byte(`[{"id": "old", "action": "delete", "when": [{"field": "age_days", "op": "gte", "value": 1e400}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rules.ImportRules(ctx, d, doc); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := d.Read.QueryRowContext(ctx, "SELECT conditions FROM rules").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `[{"field": "age_days", "op": "gte", "value": Infinity}]` {
		t.Fatalf("stored %s", stored)
	}
	loaded, err := rules.LoadAllRules(ctx, d.Read)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadAllRules = %v, %v", loaded, err)
	}
	if got := loaded[0].Conditions[0].JSON(); got != `{"field": "age_days", "op": "gte", "value": Infinity}` {
		t.Errorf("reloaded %s", got)
	}
	if ok, err := loaded[0].Conditions[0].Matches(row(func(m *rules.Message) { m.DateTS = 1 })); err != nil || ok {
		t.Errorf("Matches = %v, %v", ok, err)
	}
}
