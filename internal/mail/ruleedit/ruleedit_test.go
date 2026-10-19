package ruleedit_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/py"
)

func when(field, op string, value ...any) py.Dict {
	d := py.Dict{{Key: "field", Value: field}, {Key: "op", Value: op}}
	if len(value) > 0 {
		d = append(d, py.Pair{Key: "value", Value: value[0]})
	}
	return d
}

func raw(pairs ...any) py.Dict {
	var d py.Dict
	for i := 0; i < len(pairs); i += 2 {
		d = append(d, py.Pair{Key: pairs[i].(string), Value: pairs[i+1]})
	}
	return d
}

func ids(t *testing.T, d *db.DB, account string) []string {
	t.Helper()
	listed, err := ruleedit.ListRules(t.Context(), d.Read, account)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, r := range listed {
		out = append(out, r.ID)
	}
	return out
}

func wantRuleError(t *testing.T, err error, msg string) {
	t.Helper()
	var re rules.RuleError
	if !errors.As(err, &re) || (msg != "" && string(re) != msg) {
		t.Errorf("error %v, want RuleError %q", err, msg)
	}
}

func TestRuleCRUDAndReorder(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	a, err := ruleedit.CreateRule(ctx, d, raw("account", "icloud", "dest", "A", "when", []any{when("from_domain", "eq", "a.test")}))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ID) != 10 || a.ID[:2] != "r-" {
		t.Errorf("generated id %q", a.ID)
	}
	if _, err := ruleedit.CreateRule(ctx, d, raw("id", "b", "account", "icloud", "dest", "B", "when", []any{when("from_addr", "present")})); err != nil {
		t.Fatal(err)
	}
	c, err := ruleedit.CreateRule(ctx, d, raw("id", "c", "account", "gmail", "action", "delete", "when", []any{when("list_id", "eq", "x")}))
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(t, d, "icloud"); !slices.Equal(got, []string{a.ID, "b"}) {
		t.Errorf("icloud rules %v", got)
	}
	if err := ruleedit.ReorderRules(ctx, d, []string{"b", a.ID}); err != nil {
		t.Fatal(err)
	}
	if got := ids(t, d, "icloud"); !slices.Equal(got, []string{"b", a.ID}) {
		t.Errorf("reordered %v", got)
	}
	all, _ := rules.LoadAllRules(ctx, d.Read)
	if len(all) != 3 || all[0].ID != "b" || all[1].ID != a.ID || all[2].ID != "c" {
		t.Errorf("all rules %+v", all)
	}
	if _, err := ruleedit.UpdateRule(ctx, d, "b", raw("account", "icloud", "dest", "B2", "when", []any{when("subject", "re", "inv")})); err != nil {
		t.Fatal(err)
	}
	listed, _ := ruleedit.ListRules(ctx, d.Read, "icloud")
	if listed[0].Dest == nil || *listed[0].Dest != "B2" {
		t.Errorf("updated rule %+v", listed[0])
	}
	body, _ := json.Marshal(listed[0])
	if string(body) != `{"id":"b","account":"icloud","action":"file","dest":"B2","when":[{"field":"subject","op":"re","value":"inv"}]}` {
		t.Errorf("rule JSON %s", body)
	}
	if ok, err := ruleedit.DeleteRule(ctx, d, c.ID); err != nil || !ok {
		t.Errorf("delete = %v, %v", ok, err)
	}
	if ok, err := ruleedit.DeleteRule(ctx, d, c.ID); err != nil || ok {
		t.Errorf("second delete = %v, %v", ok, err)
	}
	_, err = ruleedit.CreateRule(ctx, d, raw("id", "b", "dest", "X", "when", []any{when("subject", "eq", "s")}))
	wantRuleError(t, err, "rule 'b' already exists")
	_, err = ruleedit.CreateRule(ctx, d, raw("account", "icloud", "dest", "X", "when", []any{when("nope", "eq", "s")}))
	wantRuleError(t, err, "unknown field 'nope'")
	_, err = ruleedit.CreateRule(ctx, d, raw("account", "nobody", "dest", "X", "when", []any{when("subject", "eq", "s")}))
	wantRuleError(t, err, "unknown account 'nobody'")
	_, err = ruleedit.UpdateRule(ctx, d, "missing", raw("dest", "X", "when", []any{when("subject", "eq", "s")}))
	wantRuleError(t, err, "unknown rule 'missing'")
	wantRuleError(t, ruleedit.ReorderRules(ctx, d, []string{"b", "missing"}), "unknown rule id in order")
	wantRuleError(t, ruleedit.ReorderRules(ctx, d, []string{"b", "b"}), "duplicate rule ids")
	if got := ids(t, d, "icloud"); !slices.Equal(got, []string{"b", a.ID}) {
		t.Errorf("failed reorder changed %v", got)
	}
}

func TestPreviewRuleCountsAndSample(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	for i := range 5 {
		rulestest.Filed(t, d, i+1, "Shop", "s"+string(rune('0'+i))+"@shop.test", "shop.test", nil,
			rulestest.Fields{"subject": "order " + string(rune('0'+i)), "date_ts": float64(i)})
	}
	rulestest.Inbox(t, d, 20, "z@shop.test", "shop.test", nil, rulestest.Fields{"subject": "order z", "date_ts": 99.0})
	rulestest.Filed(t, d, 30, "Other", "o@other.test", "other.test", nil, nil)
	rulestest.Add(t, d, 31, nil, "", rulestest.Fields{"account": "gmail", "mailbox_id": 2, "from_addr": "g@shop.test", "from_domain": "shop.test"})
	got, err := ruleedit.PreviewRule(ctx, d.Read,
		raw("account", "icloud", "dest", "Shop", "when", []any{when("from_domain", "suffix", "shop.test")}), 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 6 || got.Total != 7 || got.InInbox != 1 || len(got.Sample) != 3 {
		t.Fatalf("preview %+v", got)
	}
	for i, subject := range []string{"order z", "order 4", "order 3"} {
		if got.Sample[i].Subject != subject {
			t.Errorf("sample %d = %q, want %q", i, got.Sample[i].Subject, subject)
		}
	}
	if got.Sample[0].Folder != "INBOX" || got.Sample[1].Folder != "Shop" {
		t.Errorf("sample folders %q %q", got.Sample[0].Folder, got.Sample[1].Folder)
	}
	_, err = ruleedit.PreviewRule(ctx, d.Read, raw("dest", "X", "when", []any{when("subject", "re", "(")}), ruleedit.DefaultLimit)
	wantRuleError(t, err, "invalid regex '(': missing ), unterminated subpattern at position 0")
	all, err := ruleedit.PreviewRule(ctx, d.Read, raw("dest", "X", "when", []any{when("from_domain", "suffix", "shop.test")}), 0)
	if err != nil || all.Count != 7 || all.Total != 8 || len(all.Sample) != 0 {
		t.Errorf("unscoped preview %+v, %v", all, err)
	}
}

func TestLearnedSummaryAndFeatures(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	for i := range 4 {
		rulestest.Filed(t, d, i+1, "Bills", "pay@bank.test", "bank.test", nil, rulestest.Fields{"subject_tmpl": "statement <n>"})
	}
	rulestest.Filed(t, d, 10, "Mixed", "a@mix.test", "mix.test", nil, nil)
	rulestest.Filed(t, d, 11, "Other", "b@mix.test", "mix.test", nil, nil)
	summary, err := ruleedit.LearnedRulesSummary(ctx, d.Read, "icloud", ruleedit.DefaultLearnedLimit)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[[2]string]ruleedit.LearnedRule{}
	for _, r := range summary {
		byKey[[2]string{r.Level, r.Key}] = r
	}
	if r := byKey[[2]string{"addr", "pay@bank.test"}]; r.N != 4 || r.Purity != 1.0 || r.Dest != "Bills" {
		t.Errorf("addr rule %+v", r)
	}
	if r := byKey[[2]string{"addr+tmpl", "pay@bank.test | statement <n>"}]; r.N != 4 {
		t.Errorf("addr+tmpl rule %+v", r)
	}
	if _, ok := byKey[[2]string{"domain", "mix.test"}]; ok {
		t.Error("mixed domain learned")
	}
	if limited, _ := ruleedit.LearnedRulesSummary(ctx, d.Read, "icloud", 1); len(limited) != 1 {
		t.Errorf("limit 1 gave %d", len(limited))
	}
	m := rulestest.Inbox(t, d, 20, "pay@bank.test", "bank.test", nil, rulestest.Fields{"list_id": "l.bank.test", "dkim_d": []string{"bank.test"}})
	rulestest.Exec(t, d, "INSERT INTO decisions (message_id, account, ts, stage, action, dest) VALUES (?, 'icloud', 0, 'knn', 'file', 'Bills')", m)
	feats, err := ruleedit.MessageFeatures(ctx, d.Read, m)
	if err != nil {
		t.Fatal(err)
	}
	if feats.Dest == nil || *feats.Dest != "Bills" || feats.Account != "icloud" {
		t.Errorf("features %+v", feats)
	}
	body, _ := json.Marshal(feats)
	for _, want := range []string{
		`"default":[{"field":"from_addr","op":"eq","value":"pay@bank.test"}]`,
		`{"field":"dkim_d","op":"in","value":["bank.test"]}`,
		`{"field":"list_id","op":"eq","value":"l.bank.test"}`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("features %s lack %s", body, want)
		}
	}
	_, err = ruleedit.MessageFeatures(ctx, d.Read, 999)
	wantRuleError(t, err, "unknown message 999")
}
