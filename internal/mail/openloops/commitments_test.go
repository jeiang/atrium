package openloops_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/py"
)

func commitment(text, owner string, due any) py.Dict {
	return py.Dict{{Key: "text", Value: text}, {Key: "owner", Value: owner}, {Key: "due", Value: due}}
}

type fakeJudge struct {
	answers map[int64]any
	seen    []int64
}

func (f *fakeJudge) judge(_ context.Context, _ config.Env, _ string, m openloops.Message, _ string) (any, error) {
	f.seen = append(f.seen, m.ID)
	if a, ok := f.answers[m.ID]; ok {
		return a, nil
	}
	return []any{}, nil
}

func TestDueDate(t *testing.T) {
	for in, want := range map[any]string{
		"2026-10-20": "2026-10-20", " 2026-10-20T09:00 ": "2026-10-20", "20261020": "2026-10-20", "next friday": "", "": "",
		nil: "", 7: "", "2026-02-30": "", "2026-W42-5": "2026-10-16",
	} {
		got, ok := openloops.DueDate(in)
		if got != want || ok != (want != "") {
			t.Errorf("DueDate(%v) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestCommitmentExtractionStoresValidItemsOnce(t *testing.T) {
	d := newDB(t)
	a := mail(t, d, 1, 1, fields{"subject": "Report"})
	b := sent(t, d, 2, 1, "pat@person.test", fields{"subject": "Promise"})
	mail(t, d, 3, 1, fields{"from_addr": "noreply@shop.test"})
	judge := &fakeJudge{answers: map[int64]any{
		a: []any{commitment("They will send the report", "them", "2026-10-20")},
		b: []any{
			commitment("I will review the draft", "me", "next friday"),
			commitment("", "me", nil),
			commitment("bad owner", "you", nil),
		},
	}}
	cfg := config.Config{}
	n, err := openloops.ScanCommitments(ctx, d, cfg, env(), NOW(), judge.judge, openloops.CommitmentScanLimit)
	if err != nil || n != 2 {
		t.Fatalf("ScanCommitments = %d, %v", n, err)
	}
	slices.Sort(judge.seen)
	if !slices.Equal(judge.seen, []int64{a, b}) {
		t.Errorf("judged %v; the automated sender must never reach the model", judge.seen)
	}
	got, err := openloops.ListCommitments(ctx, d.Read, "", openloops.DefaultListLimit)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListCommitments = %+v, %v", got, err)
	}
	type triple struct {
		owner string
		due   string
		msg   int64
	}
	have := map[string]triple{}
	for _, c := range got {
		due := ""
		if c.Due != nil {
			due = *c.Due
		}
		have[c.Text] = triple{c.Owner, due, c.MessageID}
	}
	if have["They will send the report"] != (triple{"them", "2026-10-20", a}) || have["I will review the draft"] != (triple{"me", "", b}) {
		t.Errorf("commitments = %+v", have)
	}
	if got[0].Due == nil || got[len(got)-1].Due != nil {
		t.Errorf("dated commitments must come first: %+v", got)
	}
	n, err = openloops.ScanCommitments(ctx, d, cfg, env(), NOW(), judge.judge, openloops.CommitmentScanLimit)
	if err != nil || n != 0 || len(judge.seen) != 2 {
		t.Errorf("rescan = %d, %v, judged %v", n, err, judge.seen)
	}
}

func TestMovedMailIsNotScannedForCommitmentsAgain(t *testing.T) {
	d := newDB(t)
	first := mail(t, d, 1, 1, fields{"subject": "Report"})
	answer := []any{commitment("They will send the report", "them", "2026-10-20")}
	judge := &fakeJudge{answers: map[int64]any{first: answer}}
	if n, err := openloops.ScanCommitments(ctx, d, config.Config{}, env(), NOW(), judge.judge, 20); err != nil || n != 1 {
		t.Fatalf("scan = %d, %v", n, err)
	}
	exec(t, d, "UPDATE messages SET gone_at = ? WHERE id = ?", NOW(), first)
	mail(t, d, 7, 1, fields{"subject": "Report", "mailbox_id": 2, "digest": "d1"})
	judge = &fakeJudge{}
	if n, err := openloops.ScanCommitments(ctx, d, config.Config{}, env(), NOW(), judge.judge, 20); err != nil || n != 0 || len(judge.seen) != 0 {
		t.Errorf("rescan = %d, %v, judged %v", n, err, judge.seen)
	}
	if got, _ := openloops.ListCommitments(ctx, d.Read, "", 50); len(got) != 1 {
		t.Errorf("commitments = %+v", got)
	}
}

func TestCommitmentScanDefersWhenModelDown(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 1, nil)
	down := func(context.Context, config.Env, string, openloops.Message, string) (any, error) {
		return nil, &httpx.ServiceUnavailable{Msg: "offline"}
	}
	if n, err := openloops.ScanCommitments(ctx, d, config.Config{}, env(), NOW(), down, 20); err != nil || n != 0 {
		t.Fatalf("scan = %d, %v", n, err)
	}
	if got := count(t, d, "SELECT COUNT(*) FROM commitment_scans"); got != 0 {
		t.Errorf("a deferred message was marked scanned: %d", got)
	}
}

func TestUnusableCommitmentAnswerMarksScannedWithNothing(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 1, nil)
	bad := func(context.Context, config.Env, string, openloops.Message, string) (any, error) {
		return nil, httpx.ErrKey
	}
	if n, err := openloops.ScanCommitments(ctx, d, config.Config{}, env(), NOW(), bad, 20); err != nil || n != 0 {
		t.Fatalf("scan = %d, %v", n, err)
	}
	if got := count(t, d, "SELECT COUNT(*) FROM commitment_scans"); got != 1 {
		t.Errorf("commitment_scans = %d", got)
	}
}

func TestCommitmentAnswerShapes(t *testing.T) {
	for name, answer := range map[string]any{"object": py.Dict{{Key: "text", Value: "x"}}, "number": 5, "true": true} {
		d := newDB(t)
		mail(t, d, 1, 1, nil)
		judge := &fakeJudge{answers: map[int64]any{1: answer}}
		if _, err := openloops.ScanCommitments(ctx, d, config.Config{}, env(), NOW(), judge.judge, 20); !errors.Is(err, httpx.ErrType) {
			t.Errorf("%s answer: err = %v, want TypeError", name, err)
		}
		if got := count(t, d, "SELECT COUNT(*) FROM commitment_scans"); got != 0 {
			t.Errorf("%s answer marked the message scanned", name)
		}
	}
	d := newDB(t)
	mail(t, d, 1, 1, nil)
	judge := &fakeJudge{answers: map[int64]any{1: "no commitments"}}
	if n, err := openloops.ScanCommitments(ctx, d, config.Config{}, env(), NOW(), judge.judge, 20); err != nil || n != 0 {
		t.Errorf("string answer = %d, %v", n, err)
	}
	items := []any{"x", 3, py.Dict{{Key: "text", Value: 7}}}
	judge = &fakeJudge{answers: map[int64]any{1: items}}
	d2 := newDB(t)
	mail(t, d2, 1, 1, nil)
	if _, err := openloops.ScanCommitments(ctx, d2, config.Config{}, env(), NOW(), judge.judge, 20); err == nil || !strings.Contains(err.Error(), "strip") {
		t.Errorf("non-string text: err = %v, want AttributeError", err)
	}
}

func TestOnlyTheFirstFiveCommitmentsOfAMessageAreKept(t *testing.T) {
	d := newDB(t)
	id := mail(t, d, 1, 1, nil)
	var items []any
	for _, text := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		items = append(items, commitment(text, "me", nil))
	}
	judge := &fakeJudge{answers: map[int64]any{id: items}}
	if n, err := openloops.ScanCommitments(ctx, d, config.Config{}, env(), NOW(), judge.judge, 20); err != nil || n != 5 {
		t.Errorf("scan = %d, %v", n, err)
	}
}

func modelServer(t *testing.T, content string, bodies *[][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, body)
		reply, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
		_, _ = w.Write(reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func requestUser(t *testing.T, body []byte) string {
	t.Helper()
	var req struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages[1].Content
}

func TestJudgeCommitmentsUsesTheLocalModel(t *testing.T) {
	d := newDB(t)
	row := mail(t, d, 1, 1, nil)
	var bodies [][]byte
	srv := modelServer(t, `{"commitments": [{"text": "x", "owner": "me", "due": null}]}`, &bodies)
	m := loadMessage(t, d, row)
	out, err := openloops.JudgeCommitments(ctx, env(), srv.URL, m, "body text")
	if err != nil {
		t.Fatal(err)
	}
	items, ok := out.([]any)
	if !ok || len(items) != 1 || items[0].(py.Dict)[0].Value != "x" {
		t.Errorf("answer = %#v", out)
	}
	if len(bodies) != 1 || !strings.Contains(requestUser(t, bodies[0]), "body text") {
		t.Fatalf("request bodies = %q", bodies)
	}
	var req map[string]any
	_ = json.Unmarshal(bodies[0], &req)
	if req["max_tokens"] != float64(500) {
		t.Errorf("max_tokens = %v", req["max_tokens"])
	}
}

func TestJudgeCommitmentsReportsAnswersWithoutTheKey(t *testing.T) {
	d := newDB(t)
	row := mail(t, d, 1, 1, nil)
	m := loadMessage(t, d, row)
	for content, want := range map[string]error{`{"other": []}`: httpx.ErrKey, `[]`: httpx.ErrType, `"x"`: httpx.ErrType, `not json`: httpx.ErrValue} {
		var bodies [][]byte
		srv := modelServer(t, content, &bodies)
		if _, err := openloops.JudgeCommitments(ctx, env(), srv.URL, m, ""); !errors.Is(err, want) {
			t.Errorf("answer %s: err = %v, want %v", content, err, want)
		}
	}
}

func TestCommitmentDateFollowsAtriumTZ(t *testing.T) {
	d := newDB(t)
	moment := float64(time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC).Unix())
	row := mail(t, d, 1, 0, fields{"date_ts": moment})
	var bodies [][]byte
	srv := modelServer(t, `{"commitments": []}`, &bodies)
	tokyo := config.Map(map[string]string{"ATRIUM_TZ": "Asia/Tokyo"})
	if _, err := openloops.JudgeCommitments(ctx, tokyo, srv.URL, loadMessage(t, d, row), ""); err != nil {
		t.Fatal(err)
	}
	if user := requestUser(t, bodies[0]); !strings.Contains(user, "on 2026-11-01.") {
		t.Errorf("user prompt = %q", user)
	}
}

func TestSetCommitmentStatus(t *testing.T) {
	d := newDB(t)
	id := mail(t, d, 1, 1, nil)
	exec(t, d, "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'Call back', 'me', '2026-10-20', ?)", id, NOW())
	if ok, err := openloops.SetCommitmentStatus(ctx, d.Write, 1, openloops.Done); err != nil || !ok {
		t.Fatalf("SetCommitmentStatus = %v, %v", ok, err)
	}
	if ok, err := openloops.SetCommitmentStatus(ctx, d.Write, 9, openloops.Done); err != nil || ok {
		t.Errorf("unknown commitment = %v, %v", ok, err)
	}
	if _, err := openloops.SetCommitmentStatus(ctx, d.Write, 1, "bogus"); !errors.Is(err, httpx.ErrValue) {
		t.Errorf("bogus status = %v", err)
	}
	if got, _ := openloops.ListCommitments(ctx, d.Read, openloops.Open, 50); len(got) != 0 {
		t.Errorf("open = %+v", got)
	}
	if got, _ := openloops.ListCommitments(ctx, d.Read, openloops.Done, 50); len(got) != 1 || got[0].Subject == nil || *got[0].Subject != "Hello" {
		t.Errorf("done = %+v", got)
	}
}

func loadMessage(t *testing.T, d *db.DB, id int64) openloops.Message {
	t.Helper()
	var m openloops.Message
	err := d.Read.QueryRow("SELECT id, account, msgid, date_ts, sent, subject, from_name, from_addr, to_addrs, list_id, precedence, category, labels, flags "+
		"FROM messages WHERE id = ?", id).Scan(&m.ID, &m.Account, &m.MsgID, &m.DateTS, &m.Sent, &m.Subject, &m.FromName, &m.FromAddr, &m.ToAddrs,
		&m.ListID, &m.Precedence, &m.Category, &m.Labels, &m.Flags)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
