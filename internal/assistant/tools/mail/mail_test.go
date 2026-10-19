package mailtools_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant/tool"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/py"
)

func embedInvoice(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, text := range texts {
		index := 5
		if strings.Contains(strings.ToLower(text), "invoice") {
			index = 0
		}
		v := rulestest.Unit(index, -1, 0)
		out[i] = make([]float64, len(v))
		for j, x := range v {
			out[i][j] = float64(x)
		}
	}
	return out, nil
}

func mailWorld(t *testing.T) (*db.DB, *tool.Context, int64, int64) {
	t.Helper()
	d := rulestest.DB(t)
	a := rulestest.Filed(t, d, 1, "Bills", "billing@acme.test", "example.test", nil, rulestest.Fields{
		"subject": "March invoice", "body_len": 10, "from_name": "Acme Billing", "to_addrs": []string{"me@example.test"},
		"date_ts": 1_700_000_000.0,
	})
	rulestest.Exec(t, d, "UPDATE message_fts SET body = ? WHERE rowid = ?", strings.Repeat("Your invoice total is $40. ", 40), a)
	b := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", nil, rulestest.Fields{"subject": "Lunch", "date_ts": 1_710_000_000.0})
	rulestest.Exec(t, d, "UPDATE message_fts SET body = ? WHERE rowid = ?", "Want to grab lunch?", b)
	tc := ctxFor(d, config.Config{}, nil)
	tc.Embed = embedInvoice
	return d, tc, a, b
}

func resultIDs(t *testing.T, out py.Dict) []int64 {
	t.Helper()
	ids := []int64{}
	for _, r := range list(t, get(t, out, "results")) {
		ids = append(ids, get(t, r.(py.Dict), "message_id").(int64))
	}
	return ids
}

func TestMailSearchReturnsBoundedRows(t *testing.T) {
	_, tc, a, _ := mailWorld(t)
	out := mustCall(t, tc, "mail_search", `{"query": "invoice", "limit": 500}`)
	if ids := resultIDs(t, out); len(ids) != 1 || ids[0] != a {
		t.Fatalf("ids = %v", ids)
	}
	row := list(t, get(t, out, "results"))[0].(py.Dict)
	if got := get(t, row, "from"); got != "Acme Billing <billing@acme.test>" {
		t.Errorf("from = %v", got)
	}
	if got := py.JSONDumps(get(t, row, "to")); got != `["me@example.test"]` {
		t.Errorf("to = %s", got)
	}
	if get(t, row, "folder") != "Bills" || !strings.HasPrefix(get(t, row, "date").(string), "2023-11") {
		t.Errorf("row = %s", py.JSONDumps(row))
	}
	if get(t, row, "date") != "2023-11-14T22:13:20+00:00" {
		t.Errorf("date = %v", get(t, row, "date"))
	}
	if n := utf8.RuneCountInString(get(t, row, "snippet").(string)); n > mailtools.SnippetChars {
		t.Errorf("snippet is %d characters", n)
	}
	if get(t, out, "semantic") != true {
		t.Errorf("semantic = %v", get(t, out, "semantic"))
	}
	var keys []string
	for _, p := range row {
		keys = append(keys, p.Key)
	}
	if got := strings.Join(keys, ","); got != "message_id,account,folder,date,from,to,subject,snippet" {
		t.Errorf("key order %s", got)
	}
}

func TestMailSearchFromWithoutNameIsBareAddress(t *testing.T) {
	_, tc, _, b := mailWorld(t)
	out := mustCall(t, tc, "mail_search", `{"query": "lunch"}`)
	row := list(t, get(t, out, "results"))[0].(py.Dict)
	if get(t, row, "message_id") != b || get(t, row, "from") != "new@sender.test" || py.JSONDumps(get(t, row, "to")) != "[]" {
		t.Errorf("row = %s", py.JSONDumps(row))
	}
}

func TestMailSearchFiltersAndDegradesWithoutEmbedder(t *testing.T) {
	_, tc, _, b := mailWorld(t)
	tc.Embed = nil
	out := mustCall(t, tc, "mail_search", `{"query": "lunch", "since": "2024-01-01"}`)
	if ids := resultIDs(t, out); len(ids) != 1 || ids[0] != b {
		t.Errorf("ids = %v", ids)
	}
	if get(t, out, "semantic") != false {
		t.Errorf("semantic = %v", get(t, out, "semantic"))
	}
	if ids := resultIDs(t, mustCall(t, tc, "mail_search", `{"query": "lunch", "until": "2023-12-31"}`)); len(ids) != 0 {
		t.Errorf("until filter left %v", ids)
	}
	_, err := call(t, tc, "mail_search", `{"query": "x", "since": "yesterday"}`)
	wantToolError(t, err, "bad date filter: time data 'yesterday' does not match format '%Y-%m-%d'")
}

func TestMailSearchArgumentErrors(t *testing.T) {
	_, tc, _, _ := mailWorld(t)
	for args, want := range map[string]string{
		`{}`:                           "query is required",
		`{"query": 3}`:                 "query must be a string",
		`{"query": "x", "limit": "a"}`: "limit must be a number",
		`{"query": "x", "account": 1}`: "account must be a string",
	} {
		_, err := call(t, tc, "mail_search", args)
		wantToolError(t, err, want)
	}
}

func TestMailReadIsUntrustedAndCapped(t *testing.T) {
	_, tc, a, _ := mailWorld(t)
	out := mustCall(t, tc, "mail_read", `{"message_id": `+itoa(a)+`, "max_chars": 50}`)
	if get(t, out, "untrusted") != true || get(t, out, "truncated") != true {
		t.Errorf("out = %s", py.JSONDumps(out))
	}
	if n := utf8.RuneCountInString(get(t, out, "body").(string)); n != 50 {
		t.Errorf("body is %d characters", n)
	}
	if got := py.JSONDumps(get(t, out, "attachments")); got != "[]" {
		t.Errorf("attachments = %s", got)
	}
	headers := get(t, out, "headers").(py.Dict)
	if subject, _ := headers.Get("Subject"); subject != "March invoice" {
		t.Errorf("headers = %s", py.JSONDumps(headers))
	}
	if _, err := call(t, tc, "mail_read", `{"message_id": 999}`); err == nil {
		t.Error("an unknown message did not fail")
	} else {
		wantToolError(t, err, "no message 999")
	}
	_ = py.JSONDumps(out)
}

func TestMailReadArgumentErrors(t *testing.T) {
	_, tc, _, _ := mailWorld(t)
	for _, args := range []string{`{}`, `{"message_id": true}`, `{"message_id": 1.0}`, `{"message_id": "1"}`} {
		_, err := call(t, tc, "mail_read", args)
		wantToolError(t, err, "message_id must be an integer")
	}
	_, err := call(t, tc, "mail_read", `{"message_id": 99999999999999999999, "max_chars": "x"}`)
	wantToolError(t, err, "max_chars must be a number")
	var raised *tool.Raised
	_, err = call(t, tc, "mail_read", `{"message_id": 99999999999999999999}`)
	if !asRaised(err, &raised) || raised.Class != "OverflowError" {
		t.Errorf("huge id: %v", err)
	}
}
