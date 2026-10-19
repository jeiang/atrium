package router_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func seedFolders(t *testing.T, d *db.DB) {
	t.Helper()
	uid := 1000
	for i := range 10 {
		rulestest.Filed(t, d, uid, "Alpha", fmt.Sprintf("a%d@alpha.test", i), "alpha.test", rulestest.Vector(1), nil)
		uid++
		rulestest.Filed(t, d, uid, "Beta", fmt.Sprintf("b%d@beta.test", i), "beta.test", rulestest.Vector(2), nil)
		uid++
	}
}

func never(context.Context, string, []string, string, []llm.Neighbor, llm.Message, string) (string, bool, error) {
	panic("llm must not be called")
}

func decide(t *testing.T, d *db.DB, id int64, judge router.Judge, account string) *router.Decision {
	t.Helper()
	ctx := t.Context()
	ruleList, err := rules.LoadRules(ctx, d.Read, account)
	if err != nil {
		t.Fatal(err)
	}
	lc, err := router.NewLLMContext(ctx, d.Read, account, "http://chat/v1")
	if err != nil {
		t.Fatal(err)
	}
	m := rulestest.Fetch(t, d, id)
	r := &router.Router{DB: d, Judge: judge}
	got, err := r.Decide(ctx, d.Read, &m, ruleList, &lc)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func str(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func importRules(t *testing.T, d *db.DB, text string) {
	t.Helper()
	doc, err := py.JSONLoads([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rules.ImportRules(t.Context(), d, doc); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorRuleWinsOverEverything(t *testing.T) {
	d := rulestest.DB(t)
	seedFolders(t, d)
	rulestest.Filed(t, d, 1, "Alpha", "x@op.test", "op.test", nil, nil)
	for i := range 3 {
		rulestest.Filed(t, d, 10+i, "Alpha", "x@op.test", "op.test", nil, nil)
	}
	m := rulestest.Inbox(t, d, 2, "x@op.test", "op.test", rulestest.Vector(2), nil)
	importRules(t, d, `[{"id": "r", "dest": "Ops", "when": [{"field": "from_domain", "op": "eq", "value": "op.test"}]}]`)
	got := decide(t, d, m, never, "icloud")
	if got.Stage != "operator" || str(got.Dest) != "Ops" || str(got.Key) != "r" {
		t.Errorf("decision %+v", got)
	}
}

func TestOperatorDeleteAction(t *testing.T) {
	d := rulestest.DB(t)
	m := rulestest.Inbox(t, d, 2, "x@op.test", "op.test", nil, nil)
	importRules(t, d, `[{"id": "d", "action": "delete", "when": [{"field": "from_domain", "op": "eq", "value": "op.test"}]}]`)
	got := decide(t, d, m, never, "icloud")
	if got.Stage != "operator" || got.Action != "delete" || got.Dest != nil {
		t.Errorf("decision %+v", got)
	}
}

func TestLearnedBeforeKNN(t *testing.T) {
	d := rulestest.DB(t)
	seedFolders(t, d)
	for i := range 3 {
		rulestest.Filed(t, d, 10+i, "Beta", "known@seen.test", "seen.test", rulestest.Vector(2), nil)
	}
	m := rulestest.Inbox(t, d, 2, "known@seen.test", "seen.test", rulestest.Vector(1), nil)
	got := decide(t, d, m, never, "icloud")
	if got.Stage != "learned" || str(got.Dest) != "Beta" || str(got.Key) != "addr" {
		t.Errorf("decision %+v", got)
	}
	if got.Confidence == nil || *got.Confidence != 1.0 || !reflect.DeepEqual(got.Detail, py.Dict{{Key: "n", Value: 3}}) {
		t.Errorf("confidence/detail %+v", got)
	}
}

func TestKNNWhenVoteShareHigh(t *testing.T) {
	d := rulestest.DB(t)
	seedFolders(t, d)
	m := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", rulestest.Vector(1), nil)
	got := decide(t, d, m, never, "icloud")
	if got.Stage != "knn" || str(got.Dest) != "Alpha" || str(got.Key) != "vote" || *got.Confidence < 0.9 {
		t.Errorf("decision %+v", got)
	}
}

func TestLLMWhenVoteSplitAndFallbackWhenUncertain(t *testing.T) {
	d := rulestest.DB(t)
	seedFolders(t, d)
	m := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", rulestest.Unit(1, 2, 1.0), nil)
	var folders []string
	var catalog string
	var neighbors int
	judge := func(_ context.Context, _ string, f []string, c string, n []llm.Neighbor, _ llm.Message, _ string) (string, bool, error) {
		folders, catalog, neighbors = f, c, len(n)
		return "Beta", true, nil
	}
	got := decide(t, d, m, judge, "icloud")
	if got.Stage != "llm" || str(got.Dest) != "Beta" || str(got.Key) != "chat" || !reflect.DeepEqual(got.Detail, py.Dict{{Key: "confident", Value: true}}) {
		t.Errorf("decision %+v", got)
	}
	if !reflect.DeepEqual(folders, []string{"Alpha", "Beta"}) || neighbors != 10 || !strings.HasPrefix(catalog, "- Alpha (top senders: alpha.test)") {
		t.Errorf("judge saw %v %q %d", folders, catalog, neighbors)
	}
	got = decide(t, d, m, func(context.Context, string, []string, string, []llm.Neighbor, llm.Message, string) (string, bool, error) {
		return llm.Uncertain, false, nil
	}, "icloud")
	if got.Stage != "fallback" || str(got.Dest) != "Misc" || got.Action != "file" {
		t.Errorf("fallback %+v", got)
	}
}

func TestGmailUncertainProposesNoLabel(t *testing.T) {
	d := rulestest.DB(t)
	gmail := rulestest.Fields{"account": "gmail", "mailbox_id": 2}
	for i := range 10 {
		rulestest.Filed(t, d, 100+i, "Alpha", fmt.Sprintf("a%d@a.test", i), "a.test", rulestest.Vector(1), gmail)
		rulestest.Filed(t, d, 200+i, "Beta", fmt.Sprintf("b%d@b.test", i), "b.test", rulestest.Vector(2), gmail)
	}
	m := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", rulestest.Unit(1, 2, 1.0), gmail)
	got := decide(t, d, m, func(context.Context, string, []string, string, []llm.Neighbor, llm.Message, string) (string, bool, error) {
		return llm.Uncertain, false, nil
	}, "gmail")
	if got.Stage != "fallback" || got.Action != "none" || got.Dest != nil {
		t.Errorf("decision %+v", got)
	}
}

func TestKNNIsAccountScoped(t *testing.T) {
	d := rulestest.DB(t)
	seedFolders(t, d)
	for i := range 10 {
		rulestest.Filed(t, d, 300+i, "Gmailish", fmt.Sprintf("g%d@g.test", i), "g.test", rulestest.Vector(3), rulestest.Fields{"account": "gmail", "mailbox_id": 2})
	}
	m := rulestest.Fetch(t, d, rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", rulestest.Vector(3), nil))
	neighbors, err := router.NearestFiled(t.Context(), d.Read, &m, 7)
	if err != nil || len(neighbors) == 0 {
		t.Fatalf("neighbors %v, %v", neighbors, err)
	}
	for _, n := range neighbors {
		if n.Folder != "Alpha" && n.Folder != "Beta" {
			t.Errorf("neighbor in %q", n.Folder)
		}
	}
}

func TestUnembeddedMessageIsDeferredAfterRuleStages(t *testing.T) {
	d := rulestest.DB(t)
	seedFolders(t, d)
	m := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", nil, nil)
	if got := decide(t, d, m, never, "icloud"); got != nil {
		t.Errorf("decision %+v", got)
	}
}

func TestNoFiledNeighborsFallsBackWithoutLLM(t *testing.T) {
	d := rulestest.DB(t)
	m := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", rulestest.Vector(1), nil)
	if got := decide(t, d, m, never, "icloud"); got.Stage != "fallback" {
		t.Errorf("decision %+v", got)
	}
}

func TestRouterHonorsCorrectionBeforeOperatorRule(t *testing.T) {
	d := rulestest.DB(t)
	m := rulestest.Inbox(t, d, 1, "x@op.test", "op.test", nil, nil)
	importRules(t, d, `[{"id": "r", "dest": "Ops", "when": [{"field": "from_domain", "op": "eq", "value": "op.test"}]}]`)
	rulestest.Exec(t, d, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Mine', 0, 'operator')", m)
	got := decide(t, d, m, never, "icloud")
	if got.Stage != "correction" || str(got.Dest) != "Mine" || str(got.Key) != "operator" || *got.Confidence != 1.0 {
		t.Errorf("decision %+v", got)
	}
}

func newRouter(d *db.DB, judge router.Judge) *router.Router {
	return &router.Router{DB: d, ChatURL: "http://chat/v1", Judge: judge, Now: func() float64 { return 1792080000 }}
}

func TestRouteAccountEligibilityAndIdempotence(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	seedFolders(t, d)
	good := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", rulestest.Vector(1), nil)
	rulestest.Inbox(t, d, 3, "new@sender.test", "sender.test", rulestest.Vector(1), rulestest.Fields{"seen": 0})
	rulestest.Inbox(t, d, 4, "new@sender.test", "sender.test", rulestest.Vector(1), rulestest.Fields{"flagged": 1})
	rulestest.Inbox(t, d, 5, "new@sender.test", "sender.test", rulestest.Vector(1), rulestest.Fields{"gone_at": 1.0})
	rulestest.Filed(t, d, 6, "Alpha", "z@z.test", "example.test", nil, nil)
	r := newRouter(d, never)
	for want := range []int{1, 0} {
		n, err := r.RouteAccount(ctx, "icloud")
		if err != nil || n != []int{1, 0}[want] {
			t.Fatalf("RouteAccount = %d, %v", n, err)
		}
	}
	var id int64
	var stage, key, action, dest, detail string
	var conf, ts float64
	err := d.Read.QueryRowContext(ctx, "SELECT message_id, stage, key, action, dest, confidence, ts, detail FROM decisions").Scan(&id, &stage, &key, &action, &dest, &conf, &ts, &detail)
	if err != nil || id != good || stage != "knn" || key != "vote" || action != "file" || dest != "Alpha" || conf < 0.9 || ts != 1792080000 || detail != "{}" {
		t.Errorf("decision row %d %s %s %s %s %v %v %s, %v", id, stage, key, action, dest, conf, ts, detail, err)
	}
	rulestest.Exec(t, d, "UPDATE messages SET seen = 1 WHERE uid = 3")
	if n, err := r.RouteAccount(ctx, "icloud"); err != nil || n != 1 {
		t.Errorf("after reading: %d, %v", n, err)
	}
}

func decidedIDs(t *testing.T, d *db.DB) []int64 {
	t.Helper()
	rows, err := d.Read.QueryContext(t.Context(), "SELECT message_id FROM decisions ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
}

func TestChatOutageDefersLLMStageOnly(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	seedFolders(t, d)
	ambiguous := rulestest.Inbox(t, d, 2, "new@sender.test", "sender.test", rulestest.Unit(1, 2, 1.0), nil)
	rulestest.Inbox(t, d, 3, "o@o.test", "o.test", rulestest.Unit(1, 2, 1.0), nil)
	easy := rulestest.Inbox(t, d, 4, "e@e.test", "e.test", rulestest.Vector(2), nil)
	calls := 0
	down := func(context.Context, string, []string, string, []llm.Neighbor, llm.Message, string) (string, bool, error) {
		calls++
		return "", false, &httpx.ServiceUnavailable{Msg: "down"}
	}
	var buf bytes.Buffer
	r := newRouter(d, down)
	r.Log = logTo(&buf)
	if n, err := r.RouteAccount(ctx, "icloud"); err != nil || n != 1 {
		t.Fatalf("RouteAccount = %d, %v", n, err)
	}
	if ids := decidedIDs(t, d); len(ids) != 1 || ids[0] != easy || ids[0] == ambiguous {
		t.Errorf("decided %v", ids)
	}
	if calls != 1 {
		t.Errorf("judge called %d times after the outage began", calls)
	}
	if got, want := buf.String(), "level=WARN msg=\"chat unavailable, deferring icloud: down\"\n"; got != want {
		t.Errorf("log %q, want %q", got, want)
	}
	up := newRouter(d, func(context.Context, string, []string, string, []llm.Neighbor, llm.Message, string) (string, bool, error) {
		return "Alpha", true, nil
	})
	if n, err := up.RouteAccount(ctx, "icloud"); err != nil || n != 2 {
		t.Errorf("after recovery: %d, %v", n, err)
	}
}

func llmFixture(t *testing.T) *db.DB {
	t.Helper()
	d := rulestest.DB(t)
	uid := 1000
	for i := range 10 {
		rulestest.Filed(t, d, uid, "Alpha", fmt.Sprintf("a%d@alpha.test", i), "alpha.test",
			rulestest.Unit(1, 3, float32(0.01*float64(i))), rulestest.Fields{"subject": fmt.Sprintf("Alpha invoice %d", i)})
		rulestest.Filed(t, d, uid+1, "Beta", fmt.Sprintf("b%d@beta.test", i), "beta.test",
			rulestest.Unit(2, 4, float32(0.01*float64(i)+0.005)), rulestest.Fields{"subject": fmt.Sprintf("Beta newsletter %d", i)})
		uid += 2
	}
	rulestest.Add(t, d, 2, rulestest.Unit(1, 2, 1.0), "Your invoice for October is attached.", rulestest.Fields{
		"from_addr": "new@sender.test", "from_domain": "sender.test", "seen": 1, "flagged": 0, "in_inbox": 1, "location": "INBOX",
		"subject": "October invoice", "from_name": "New Sender",
	})
	return d
}

func TestGoldenRouterFolderRequest(t *testing.T) {
	d := llmFixture(t)
	url := golden.Replay(t, golden.LoadScenario(t, "llm", "router_folder").Exchanges).URL + "/v1"
	r := &router.Router{DB: d, ChatURL: url, Now: func() float64 { return 1 }}
	if n, err := r.RouteAccount(t.Context(), "icloud"); err != nil || n != 1 {
		t.Fatalf("RouteAccount = %d, %v", n, err)
	}
	var stage, key, dest, detail string
	if err := d.Read.QueryRowContext(t.Context(), "SELECT stage, key, dest, detail FROM decisions").Scan(&stage, &key, &dest, &detail); err != nil {
		t.Fatal(err)
	}
	if stage != "llm" || key != "chat" || dest != "Alpha" || detail != `{"confident": true}` {
		t.Errorf("decision %s %s %s %s", stage, key, dest, detail)
	}
}

func chatServer(t *testing.T, status int, body string) (url string, calls *int) {
	t.Helper()
	n := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*n++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", n
}

func TestDegradedChatDownDefersAccount(t *testing.T) {
	d := llmFixture(t)
	url, calls := chatServer(t, 503, "unavailable")
	var buf bytes.Buffer
	r := &router.Router{DB: d, ChatURL: url, Log: logTo(&buf)}
	if n, err := r.RouteAccount(t.Context(), "icloud"); err != nil || n != 0 {
		t.Fatalf("RouteAccount = %d, %v", n, err)
	}
	if ids := decidedIDs(t, d); len(ids) != 0 {
		t.Errorf("decisions %v", ids)
	}
	if *calls != 1 {
		t.Errorf("%d chat requests", *calls)
	}
	prefix := "level=WARN msg=\"chat unavailable, deferring icloud: " + url + "/chat/completions: HTTP Error 503"
	if !strings.HasPrefix(buf.String(), prefix) {
		t.Errorf("log %q lacks %q", buf.String(), prefix)
	}
}

func TestMalformedChatReplyPropagates(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		kind error
	}{
		"missing key":      {`{"choices":[{"message":{"content":"{}"}}]}`, httpx.ErrKey},
		"content not json": {`{"choices":[{"message":{"content":"nope"}}]}`, httpx.ErrValue},
	} {
		t.Run(name, func(t *testing.T) {
			d := llmFixture(t)
			url, _ := chatServer(t, 200, c.body)
			var buf bytes.Buffer
			r := &router.Router{DB: d, ChatURL: url, Log: logTo(&buf)}
			n, err := r.RouteAccount(t.Context(), "icloud")
			if !errors.Is(err, c.kind) || n != 0 {
				t.Errorf("RouteAccount = %d, %v; want %v", n, err, c.kind)
			}
			if ids := decidedIDs(t, d); len(ids) != 0 || buf.Len() != 0 {
				t.Errorf("decisions %v, log %q", ids, buf.String())
			}
		})
	}
}

func TestStageOrder(t *testing.T) {
	if router.Stages != [...]string{"correction", "operator", "learned", "knn", "llm", "fallback"} {
		t.Errorf("Stages = %v", router.Stages)
	}
}

func TestMessageBody(t *testing.T) {
	d := rulestest.DB(t)
	id := rulestest.Add(t, d, 1, nil, "the body", nil)
	if got, err := router.MessageBody(t.Context(), d.Read, id); err != nil || got != "the body" {
		t.Errorf("MessageBody = %q, %v", got, err)
	}
	if got, err := router.MessageBody(t.Context(), d.Read, 999); err != nil || got != "" {
		t.Errorf("MessageBody of nothing = %q, %v", got, err)
	}
}

func TestVote(t *testing.T) {
	folder, share, ok := router.Vote([]router.Neighbor{{Folder: "B", Similarity: 0.5}, {Folder: "A", Similarity: 0.5}, {Folder: "B", Similarity: 0.25}})
	if !ok || folder != "B" || share != 0.75/1.25 {
		t.Errorf("Vote = %q, %v, %v", folder, share, ok)
	}
	folder, _, ok = router.Vote([]router.Neighbor{{Folder: "A", Similarity: 0.5}, {Folder: "B", Similarity: 0.5}})
	if !ok || folder != "B" {
		t.Errorf("tie went to %q", folder)
	}
	if _, _, ok := router.Vote([]router.Neighbor{{Folder: "A", Similarity: 0}}); ok {
		t.Error("zero similarity voted")
	}
	if _, _, ok := router.Vote(nil); ok {
		t.Error("no neighbours voted")
	}
}

func TestCorrectionIsEligibleOnlyOnce(t *testing.T) {
	d := rulestest.DB(t)
	m := rulestest.Inbox(t, d, 1, "new@sender.test", "sender.test", nil, nil)
	if _, err := corrections.Record(t.Context(), d, m, "Bills", corrections.Source, 1); err != nil {
		t.Fatal(err)
	}
	if n, err := newRouter(d, never).RouteAccount(t.Context(), "icloud"); err != nil || n != 0 {
		t.Errorf("RouteAccount = %d, %v", n, err)
	}
}

func TestGoldenPipelineDecisionDetails(t *testing.T) {
	var rows []struct {
		Stage  string `json:"stage"`
		Detail string `json:"detail"`
	}
	raw, err := os.ReadFile(filepath.Join(golden.Dir(), "pipeline", "results", "decisions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		var detail py.Dict
		switch row.Stage {
		case "learned":
			var n struct{ N int }
			if err := json.Unmarshal([]byte(row.Detail), &n); err != nil {
				t.Fatal(err)
			}
			detail = py.Dict{{Key: "n", Value: n.N}}
		case "llm":
			detail = py.Dict{{Key: "confident", Value: strings.Contains(row.Detail, "true")}}
		}
		if got := py.JSONDumps(detail); got != row.Detail {
			t.Errorf("%s detail %q, recorded %q", row.Stage, got, row.Detail)
		}
		seen[row.Stage] = true
	}
	for _, stage := range router.Stages {
		if !seen[stage] {
			t.Errorf("pipeline golden has no %s decision", stage)
		}
	}
}
