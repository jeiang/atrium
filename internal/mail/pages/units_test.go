package pages

import (
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
	"github.com/jeiang/atrium/internal/web"
)

var inventory = [][2]string{
	{"GET", "/messages/{message_id}"},
	{"GET", "/messages/{message_id}/correct"},
	{"POST", "/messages/{message_id}/correct"},
	{"POST", "/live/pause"},
	{"POST", "/live/resume"},
	{"POST", "/live/actions/{action_id}/undo"},
	{"GET", "/triage"},
	{"GET", "/rules"},
	{"GET", "/rules/new"},
	{"GET", "/rules/condition"},
	{"POST", "/rules/preview"},
	{"POST", "/rules"},
	{"GET", "/rules/{rule_id}/edit"},
	{"POST", "/rules/{rule_id}"},
	{"POST", "/rules/{rule_id}/move"},
	{"DELETE", "/rules/{rule_id}"},
}

func TestRouteTableIsTheInventory(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	r := chi.NewRouter()
	Module(Options{})(r, &web.Deps{Cfg: s.cfg, Env: s.env, DB: s.db})
	var got [][2]string
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, [2]string{method, route})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	less := func(a, b [2]string) bool { return a[1]+a[0] < b[1]+b[0] }
	sorted := func(l [][2]string) [][2]string {
		out := append([][2]string(nil), l...)
		for i := range out {
			for j := i + 1; j < len(out); j++ {
				if less(out[j], out[i]) {
					out[i], out[j] = out[j], out[i]
				}
			}
		}
		return out
	}
	if !reflect.DeepEqual(sorted(got), sorted(inventory)) {
		t.Errorf("routes = %v\nwant     %v", sorted(got), sorted(inventory))
	}
}

func TestEveryInventoryRouteAnswers(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	for _, route := range inventory {
		path := route[1]
		for _, p := range [][2]string{{"{message_id}", s.id(s.ids.invoice)}, {"{action_id}", "999"}, {"{rule_id}", "ghost"}} {
			path = strings.ReplaceAll(path, p[0], p[1])
		}
		rec := s.do(route[0], path, map[string][]string{"direction": {"up"}, "action": {"file"}})
		if rec.Code == http.StatusForbidden || rec.Code == http.StatusMethodNotAllowed || rec.Code >= 500 {
			t.Errorf("%s %s = %d", route[0], path, rec.Code)
		}
	}
}

func TestRuleProblem(t *testing.T) {
	for in, want := range map[string]string{
		"r1: file rule needs dest":                   "Choose a destination, or set the action to Delete.",
		"file rule needs dest":                       "Choose a destination, or set the action to Delete.",
		"when must be a non-empty list":              "Add at least one condition.",
		"x: op in needs a non-empty list of strings": "“is one of” needs at least one value.",
		"unknown field 'nope'":                       "Unknown field 'nope'",
		"a b: odd":                                   "A b: odd",
		"éclair":                                     "Éclair",
		"":                                           "",
	} {
		if got := ruleProblem(rules.RuleError(in)); got != want {
			t.Errorf("ruleProblem(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ruleProblem(errors.New("a:b: c")); got != "A:b: c" {
		t.Errorf("only a prefix without colons or spaces is dropped, got %q", got)
	}
}

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{"src/atrium/web/app.py:RULE_ID_PREFIX": ruleIDPrefix})
}

func TestConditionList(t *testing.T) {
	big20, _ := new(big.Int).SetString("99999999999999999999", 10)
	got := conditionList(
		[]string{"from_domain", "dkim_d", "subject", "age_days", "age_days", "from_addr", "list_id"},
		[]string{"suffix", "in", "re", "gte", "gte", "eq", "present"},
		[]string{" a.test , ,b.test", "", " raw ", " 7 ", "99999999999999999999", "x", "ignored"},
	)
	want := []any{
		map[string]any{"field": "from_domain", "op": "suffix", "value": []string{"a.test", "b.test"}},
		map[string]any{"field": "dkim_d", "op": "in", "value": []string{}},
		map[string]any{"field": "subject", "op": "re", "value": " raw "},
		map[string]any{"field": "age_days", "op": "gte", "value": int64(7)},
		map[string]any{"field": "age_days", "op": "gte", "value": big20},
		map[string]any{"field": "from_addr", "op": "eq", "value": "x"},
		map[string]any{"field": "list_id", "op": "present"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conditionList = %#v", got)
	}
	if got := conditionList([]string{"subject", "subject"}, []string{"eq"}, nil); len(got) != 1 {
		t.Errorf("zip stops at the shorter list: %v", got)
	}
	if got := conditionList([]string{"subject"}, []string{"eq"}, nil); !reflect.DeepEqual(got, []any{map[string]any{"field": "subject", "op": "eq", "value": ""}}) {
		t.Errorf("a missing value is empty: %v", got)
	}
}

func TestBackTo(t *testing.T) {
	for _, tc := range []struct {
		referer string
		want    backLink
	}{
		{"", backLink{"/chat", "Chat"}},
		{"http://example.com/triage", backLink{"/chat", "Chat"}},
		{"http://testserver/triage?stage=llm&q=%3Cx%3E#frag", backLink{"/triage?stage=llm&q=%3Cx%3E", "Decisions"}},
		{"http://testserver/chat", backLink{"/chat", "Chat"}},
		{"http://testserver/rules", backLink{"/chat", "Chat"}},
		{"http://testserver/chat/5", backLink{"/chat", "Chat"}},
	} {
		r := httptest.NewRequest("GET", "/messages/1", nil)
		r.Host = "testserver"
		r.Header.Set("Referer", tc.referer)
		if got := backTo(r); got != tc.want {
			t.Errorf("backTo(%q) = %v, want %v", tc.referer, got, tc.want)
		}
	}
}
