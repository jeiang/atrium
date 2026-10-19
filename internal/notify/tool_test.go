package notify

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
)

func args(pairs ...string) py.Dict {
	var d py.Dict
	for i := 0; i < len(pairs); i += 2 {
		d = append(d, py.Pair{Key: pairs[i], Value: pairs[i+1]})
	}
	return d
}

func toolContext(t *testing.T, env config.Env, kind tool.Kind, run int64) *tool.Context {
	t.Helper()
	return &tool.Context{DB: dbtest.Schema(t), Env: env, Kind: kind, ThreadID: 1, RunID: run, Now: fixedNow}
}

func call(t *testing.T, tc *tool.Context, a py.Dict) (any, error) {
	t.Helper()
	return Tools()[0].Handler(context.Background(), tc, a)
}

func TestToolMatchesPythonDefinition(t *testing.T) {
	for _, tl := range Tools() {
		registrytest.Verify(t, tl)
	}
}

func TestToolRunKinds(t *testing.T) {
	r := registry.New()
	if err := r.Add(Tools()...); err != nil {
		t.Fatal(err)
	}
	var offered []tool.Kind
	for _, k := range tool.AllKinds {
		found, err := r.For(k)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) == 1 {
			offered = append(offered, k)
		}
	}
	if want := []tool.Kind{tool.Chat, tool.Cron, tool.Nightly}; !slices.Equal(offered, want) {
		t.Errorf("notify is offered in %v, want %v", offered, want)
	}
}

func TestToolPostsToNtfyAndRecords(t *testing.T) {
	srv := replay(t, "notify_tool")
	tc := toolContext(t, envFor(srv.URL, true), tool.Cron, 1)
	for _, a := range []py.Dict{
		args("title", "Renewal", "body", "Domain expires Friday.", "thread_link", "7"),
		args("title", "  Bell\x07 ", "body", "Line one\nLine two\x00", "thread_link", "/chat/12"),
		args("title", "Default link", "body", "Uses this thread."),
	} {
		out, err := call(t, tc, a)
		if err != nil {
			t.Fatal(err)
		}
		want := py.Dict{{Key: "recorded", Value: true}, {Key: "delivered", Value: true}, {Key: "note", Value: nil}}
		if got := py.JSONDumps(out); got != py.JSONDumps(want) {
			t.Errorf("result = %s, want %s", got, py.JSONDumps(want))
		}
	}
	list := rows(t, tc.DB)
	if len(list) != 3 || !list[2].Delivered || *list[2].ThreadLink != "/chat/7" || *list[2].RunID != 1 || *list[0].ThreadLink != "/chat/1" {
		t.Fatalf("ledger = %+v", list)
	}
}

func TestToolWithoutNtfyStillRecords(t *testing.T) {
	tc := toolContext(t, config.Map(nil), tool.Chat, 1)
	out, err := call(t, tc, args("title", "T", "body", "B"))
	if err != nil {
		t.Fatal(err)
	}
	got := py.JSONDumps(out)
	if !strings.Contains(got, `"recorded": true`) || !strings.Contains(got, `"delivered": false`) || !strings.Contains(got, "not configured") {
		t.Errorf("result = %s", got)
	}
	if list := rows(t, tc.DB); len(list) != 1 || *list[0].ThreadLink != "/chat/1" {
		t.Errorf("ledger = %+v", list)
	}
}

func TestToolFailureIsRecordedNotRaised(t *testing.T) {
	srv := replay(t, "notify_failure")
	tc := toolContext(t, envFor(srv.URL, true), tool.Chat, 1)
	out, err := call(t, tc, args("title", "T", "body", "B"))
	if err != nil {
		t.Fatal(err)
	}
	if got := py.JSONDumps(out); got != `{"recorded": true, "delivered": false, "note": "ntfy 500"}` {
		t.Errorf("result = %s", got)
	}
}

func TestToolCapAndLinkValidation(t *testing.T) {
	srv := httptestOK(t)
	tc := toolContext(t, envFor(srv, true), tool.Chat, 1)
	for i := range PerRunCap {
		if _, err := call(t, tc, args("title", "t"+string(rune('0'+i)), "body", "b")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call(t, tc, args("title", "t4", "body", "b")); err == nil || err.Error() != "at most 3 notifications per run" {
		t.Errorf("fourth notification = %v", err)
	}
	other := *tc
	other.RunID = 2
	_, err := call(t, &other, args("title", "t", "body", "b", "thread_link", "https://evil.test/x"))
	if _, ok := tool.Message(err); !ok || !strings.Contains(err.Error(), "thread_link") {
		t.Errorf("foreign link = %v", err)
	}
}

func TestNightlyNotifyIsCappedAtOnePerRun(t *testing.T) {
	tc := toolContext(t, config.Map(nil), tool.Nightly, 7)
	if _, err := call(t, tc, args("title", "t", "body", "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tc, args("title", "t2", "body", "b")); err == nil || err.Error() != "at most 1 notification per run" {
		t.Errorf("second nightly notification = %v", err)
	}
	chat := *tc
	chat.Kind, chat.RunID = tool.Chat, 8
	for range PerRunCap {
		if _, err := call(t, &chat, args("title", "t", "body", "b")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestToolRequiresTitleAndBody(t *testing.T) {
	tc := toolContext(t, config.Map(nil), tool.Chat, 1)
	for _, a := range []py.Dict{args("body", "b"), args("title", "t"), args("title", "  ", "body", "b")} {
		if _, err := call(t, tc, a); err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%v = %v", a, err)
		}
	}
}
