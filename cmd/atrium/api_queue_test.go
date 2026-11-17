package main

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/unit"
)

func TestQueueListsEverythingThatNeedsTheOperatorWithOneCount(t *testing.T) {
	s := newStreamSite(t)
	s.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which calendar should I use?"})))
	if ran := runApp(t, "agent", "run", "--text", "plan the week"); ran.code != cli.ExitOK {
		t.Fatalf("ask run: %d %s", ran.code, ran.stderr)
	}
	var threadID int64
	if err := s.d.Read.QueryRow("SELECT thread_id FROM runs WHERE state = 'parked'").Scan(&threadID); err != nil {
		t.Fatalf("parked run: %v", err)
	}
	backlog, _, err := proposals.Create(testProposalContext(), s.d, "inbox_backlog", nil, "Archive old newsletters", map[string]any{}, map[string]any{}, "Archive old newsletters", 100)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.d.Write.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now().UTC()
	exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (900, 'permission', 'pending', 50, 'Allow web_fetch', 'fp-permission')")
	exec("INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (1, 'a', 'pat@person.test', 'Re: lease', '', 'draft', 200, 200)")
	exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('k1', 'a', 'owe_reply', 1, 150, 'pat', 'Dinner?', 150)")
	exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('k2', 'a', 'waiting_on_them', 2, 120, 'sam', 'Contract', 120)")
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, created_ts) VALUES (2, 1, 'Book the room', 'me', ?, 'open', 301)", today.AddDate(0, 0, -3).Format("2006-01-02"))
	exec("INSERT INTO packages (id, merchant, status, status_text, tracking_number, source_message_id, last_message_id, updated_ts, first_seen) VALUES (1, 'Bookshop', 'transit', 'In transit', 'TRACK', 1, 1, 500, 500)")
	added := s.do(http.MethodPost, "/api/v1/tasks", s.token, map[string]any{"title": "Renew the library card", "due": today.AddDate(0, 0, 3).Format("2006-01-02")})
	if added.status != http.StatusCreated {
		t.Fatalf("add task: %d %s", added.status, added.body)
	}
	taskID := strconv.FormatInt(number(t, apiData(t, added, http.StatusCreated)["task_id"]), 10)
	question, permission, proposal := "question:"+strconv.FormatInt(threadID, 10), "proposal:900", "proposal:"+strconv.FormatInt(backlog, 10)

	inbox := s.do(http.MethodGet, "/api/v1/inbox", s.token, nil)
	if inbox.status != http.StatusOK {
		t.Fatalf("inbox: %d %s", inbox.status, inbox.body)
	}

	browser := newWebClient(t, s.base, nil)
	browser.session("local")
	queue := func(query string) coremodule.QueuePage {
		t.Helper()
		bearer := s.do(http.MethodGet, "/api/v1/queue"+query, s.token, nil)
		cookie := browser.do(http.MethodGet, "/web/v1/queue"+query, nil)
		if bearer.body != cookie.body {
			t.Fatalf("queue%s differs between the surfaces: %.300s / %.300s", query, bearer.body, cookie.body)
		}
		if tag := bearer.header.Get("ETag"); tag == "" || s.do(http.MethodGet, "/api/v1/queue"+query, s.token, nil, "If-None-Match", tag).status != http.StatusNotModified {
			t.Fatalf("queue%s is not conditional: ETag %q", query, tag)
		}
		return strictJSON[application.Read[coremodule.QueuePage]](t, "/queue"+query, bearer).Data
	}
	needsYou := func() int {
		t.Helper()
		return int(strictJSON[coremodule.Shell](t, "/shell", s.do(http.MethodGet, "/api/v1/shell", s.token, nil)).NeedsYou)
	}
	ids := func(items []reviewapp.QueueItem) []string {
		out := []string{}
		for _, item := range items {
			out = append(out, item.ID)
		}
		return out
	}
	inOrder := func(got []string, want ...string) {
		t.Helper()
		last := -1
		for _, id := range want {
			at := slices.Index(got, id)
			if at <= last {
				t.Fatalf("queue order %v, want %v in this order", got, want)
			}
			last = at
		}
	}

	all := queue("")
	got := ids(all.Items)
	inOrder(got, permission, question, "commitment:2", "task:"+taskID, proposal, "loop:k1", "draft:1")
	for _, absent := range []string{"loop:k2", "package:1"} {
		if slices.Contains(got, absent) {
			t.Fatalf("queue lists %s, which does not need the operator: %v", absent, got)
		}
	}
	counts := all.Counts
	if all.Total != len(all.Items) || all.NextCursor != nil || counts.Ask+counts.Proposal+counts.Draft+counts.Attention != all.Total || counts.Ask < 2 || counts.Draft != 1 {
		t.Fatalf("queue totals: total %d, %d items, counts %+v", all.Total, len(all.Items), counts)
	}
	if n := needsYou(); n != all.Total {
		t.Fatalf("shell needs_you %d, queue total %d", n, all.Total)
	}
	byID := map[string]reviewapp.QueueItem{}
	for _, item := range all.Items {
		byID[item.ID] = item
	}
	if p := byID[permission]; p.Kind != reviewapp.QueueAsk || p.WaitingFor != reviewapp.WaitingPermission || p.Module != unit.Assistant || p.ProposalKind != "permission" {
		t.Fatalf("permission item: %+v", p)
	}
	backlogItem := byID[proposal]
	if backlogItem.ProposalKind != "inbox_backlog" || backlogItem.Module == "" || byID["draft:1"].ProposalKind != "" {
		t.Fatalf("proposal kind on queue items: %+v / %+v", backlogItem, byID["draft:1"])
	}
	scoped := queue("?module=" + backlogItem.Module)
	if scoped.Total != all.Total || !slices.Contains(ids(scoped.Items), proposal) || slices.Contains(ids(scoped.Items), permission) {
		t.Fatalf("queue?module=%s: total %d of %d, items %v", backlogItem.Module, scoped.Total, all.Total, ids(scoped.Items))
	}
	for _, item := range scoped.Items {
		if item.Module != backlogItem.Module {
			t.Fatalf("queue?module=%s lists %+v", backlogItem.Module, item)
		}
	}
	if none := queue("?module=nothing"); len(none.Items) != 0 || none.Total != all.Total {
		t.Fatalf("queue?module=nothing: %+v", none)
	}
	if q := byID[question]; q.Kind != reviewapp.QueueAsk || q.WaitingFor != reviewapp.WaitingAnswer || q.Title != "Which calendar should I use?" {
		t.Fatalf("question item: %+v", q)
	}
	if c := byID["commitment:2"]; c.Kind != reviewapp.QueueAttention || c.WaitingFor != reviewapp.WaitingDue || c.Due == nil || c.Module != unit.Signals {
		t.Fatalf("commitment item: %+v", c)
	}
	if d := byID["draft:1"]; d.Kind != reviewapp.QueueDraft || d.WaitingFor != reviewapp.WaitingSend || d.Source != (reviewapp.QueueSource{Type: "draft", ID: "1"}) {
		t.Fatalf("draft item: %+v", d)
	}

	var paged []string
	cursor := ""
	for range all.Total {
		page := queue("?limit=2" + cursor)
		if page.Total != all.Total || len(page.Items) > 2 {
			t.Fatalf("page: total %d, %d items", page.Total, len(page.Items))
		}
		paged = append(paged, ids(page.Items)...)
		if page.NextCursor == nil {
			break
		}
		cursor = "&cursor=" + url.QueryEscape(*page.NextCursor)
	}
	if !slices.Equal(paged, got) {
		t.Fatalf("paged %v, want %v", paged, got)
	}
	if drafts := queue("?kind=draft"); !slices.Equal(ids(drafts.Items), []string{"draft:1"}) || drafts.Total != all.Total || drafts.Counts != all.Counts {
		t.Fatalf("kind filter: %+v", drafts)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/queue?kind=parcel", s.token, nil), http.StatusUnprocessableEntity, "invalid")

	for _, id := range []string{proposal, "draft:1", question} {
		one := strictJSON[application.Read[reviewapp.QueueItem]](t, "/queue/"+id, s.do(http.MethodGet, "/api/v1/queue/"+id, s.token, nil)).Data
		if one != byID[id] {
			t.Fatalf("queue item %s: %+v, want %+v", id, one, byID[id])
		}
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/queue/proposal:999", s.token, nil), http.StatusNotFound, "not_found")

	if again := s.do(http.MethodGet, "/api/v1/inbox", s.token, nil); again.status != http.StatusOK || again.body != inbox.body {
		t.Fatalf("inbox changed after queue reads: %s / %s", inbox.body, again.body)
	}

	frames := appFrames(t, s.openApp())
	synced := syncedRevisions(t, nextAppFrame(t, frames, "sync"))
	if denied := s.do(http.MethodPost, "/api/v1/approvals/"+strconv.FormatInt(backlog, 10)+"/deny", s.token, nil); denied.status != http.StatusOK {
		t.Fatalf("deny: %d %s", denied.status, denied.body)
	}
	waitAppChange(t, frames, "review", synced["review"])
	after := queue("")
	if slices.Contains(ids(after.Items), proposal) || after.Total != all.Total-1 || needsYou() != after.Total {
		t.Fatalf("after deny: total %d (was %d), shell %d, items %v", after.Total, all.Total, needsYou(), ids(after.Items))
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/queue/"+proposal, s.token, nil), http.StatusNotFound, "not_found")

	seedSettings(t, map[string]string{module.FlagKey(unit.Mail): "false"})
	s.restart()
	browser = newWebClient(t, s.base, nil)
	browser.session("local")
	off := queue("")
	for _, item := range off.Items {
		if item.Module == unit.Mail {
			t.Fatalf("mail is off but the queue lists %+v", item)
		}
	}
	inOrder(ids(off.Items), permission, question, "task:"+taskID)
	if off.Counts.Draft != 0 || needsYou() != off.Total {
		t.Fatalf("mail off: total %d, counts %+v, shell %d", off.Total, off.Counts, needsYou())
	}
}
