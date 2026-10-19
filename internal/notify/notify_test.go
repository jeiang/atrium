package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const now = 1_780_000_000.0

func fixedNow() float64 { return now }

func replay(t *testing.T, scenario string) *httptest.Server {
	t.Helper()
	return golden.Replay(t, golden.LoadScenario(t, "ntfy", scenario).Exchanges)
}

func envFor(url string, token bool) config.Env {
	env := map[string]string{"NTFY_URL": url, "NTFY_TOPIC": "atrium"}
	if token {
		env["NTFY_TOKEN"] = "golden-ntfy-token"
	}
	return config.Map(env)
}

func str(s string) *string { return &s }

func rows(t *testing.T, d *db.DB) []Notification {
	t.Helper()
	list, err := Recent(context.Background(), d.Read, 100)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/agent/tools/notify.py:CONTROL":     control,
		"src/atrium/agent/tools/notify.py:THREAD_LINK": threadLink,
	})
}

func TestSendGoldenNotifyTool(t *testing.T) {
	srv := replay(t, "notify_tool")
	d := dbtest.Schema(t)
	env := envFor(srv.URL, true)
	ctx := context.Background()
	for _, m := range []Message{
		{Title: "Renewal", Body: "Domain expires Friday.", ThreadLink: str("7"), ThreadID: 1, RunID: 1},
		{Title: "  Bell\x07 ", Body: "Line one\nLine two\x00", ThreadLink: str("/chat/12"), ThreadID: 1, RunID: 1},
		{Title: "Default link", Body: "Uses this thread.", ThreadID: 1, RunID: 1},
	} {
		m.Now = fixedNow
		sent, err := Send(ctx, d.Write, env, m)
		if err != nil || !sent.Delivered || sent.Error != "" {
			t.Fatalf("Send(%q) = %+v, %v", m.Title, sent, err)
		}
	}
	list := rows(t, d)
	if len(list) != 3 || list[2].Title != "Renewal" || *list[2].ThreadLink != "/chat/7" || !list[2].Delivered || list[2].TS != now || *list[2].RunID != 1 {
		t.Fatalf("ledger = %+v", list)
	}
	if list[1].Title != "Bell" || list[1].Body != "Line one\nLine two" {
		t.Errorf("control characters survived: %+v", list[1])
	}
}

func TestSendGoldenFailureIsRecordedNotRaised(t *testing.T) {
	srv := replay(t, "notify_failure")
	d := dbtest.Schema(t)
	sent, err := Send(context.Background(), d.Write, envFor(srv.URL, true), Message{Title: "T", Body: "B", ThreadID: 1, RunID: 1, Now: fixedNow})
	if err != nil || sent.Delivered || sent.Error != "ntfy 500" {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	list := rows(t, d)
	if len(list) != 1 || list[0].Delivered || list[0].Error == nil || *list[0].Error != "ntfy 500" {
		t.Fatalf("ledger = %+v", list)
	}
}

func TestSendGoldenBudgetSkipWithoutTokenOrLink(t *testing.T) {
	srv := replay(t, "budget_skip")
	d := dbtest.Schema(t)
	sent, err := Send(context.Background(), d.Write, envFor(srv.URL, false), Message{
		Title: "Skipped: brief", Body: "Monthly budget is above 80%, so this scheduled run did not start.", Now: fixedNow,
	})
	if err != nil || !sent.Delivered {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	list := rows(t, d)
	if len(list) != 1 || list[0].ThreadLink != nil || list[0].ThreadID != nil || list[0].RunID != nil {
		t.Fatalf("ledger = %+v", list)
	}
}

func TestSendMediaTopicAndClick(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer srv.Close()
	d := dbtest.Schema(t)
	sent, err := Send(context.Background(), d.Write, envFor(srv.URL, false), Message{Title: "New", Body: "Lanterns", Topic: "media", Click: "https://a.test/media/downloads", Now: fixedNow})
	if err != nil || !sent.Delivered {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	want := `{"topic":"media","title":"New","message":"Lanterns","click":"https://a.test/media/downloads"}`
	if got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestSendWithoutNtfyStillRecords(t *testing.T) {
	d := dbtest.Schema(t)
	sent, err := Send(context.Background(), d.Write, config.Map(nil), Message{Title: "T", Body: "B", ThreadID: 1, RunID: 1, Now: fixedNow})
	if err != nil || sent.Delivered || !strings.Contains(sent.Error, "not configured") {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	if list := rows(t, d); len(list) != 1 || *list[0].ThreadLink != "/chat/1" {
		t.Fatalf("ledger = %+v", list)
	}
}

func TestNtfyDownIsReportedNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	d := dbtest.Schema(t)
	sent, err := Send(context.Background(), d.Write, envFor(url, true), Message{Title: "T", Body: "B", ThreadID: 1, RunID: 1, Now: fixedNow})
	if err != nil || sent.Delivered || sent.Error != "ntfy unreachable: ConnectError" {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	if list := rows(t, d); len(list) != 1 || list[0].Delivered || *list[0].Error != sent.Error {
		t.Fatalf("ledger = %+v", list)
	}
}

func TestDeliverFailureClasses(t *testing.T) {
	cases := map[string]string{
		"ntfy.test/x":     "ntfy unreachable: UnsupportedProtocol",
		"ftp://ntfy.test": "ntfy unreachable: UnsupportedProtocol",
	}
	for url, want := range cases {
		ok, text, err := Deliver(context.Background(), envFor(url, false), "t", "b", "", "")
		if err != nil || ok || text != want {
			t.Errorf("Deliver(%q) = %v, %q, %v; want %q", url, ok, text, err, want)
		}
	}
	if _, _, err := Deliver(context.Background(), envFor("http://[::1", false), "t", "b", "", ""); err == nil {
		t.Error("a malformed NTFY_URL raises in Python (InvalidURL)")
	}
}

func TestRedirectIsNotFollowedAndNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	ok, text, err := Deliver(context.Background(), envFor(srv.URL, false), "t", "b", "", "")
	if err != nil || !ok || text != "" {
		t.Fatalf("Deliver = %v, %q, %v", ok, text, err)
	}
}

func TestSendValidation(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	if _, err := Send(ctx, d.Write, config.Map(nil), Message{Title: " \x07 ", Body: "b", Now: fixedNow}); err == nil || err.Error() != "title is required" {
		t.Errorf("blank title = %v", err)
	}
	for _, raw := range []string{"https://evil.test/x", "", "/chat/", "/chat/1x", "²"} {
		_, err := Send(ctx, d.Write, config.Map(nil), Message{Title: "t", Body: "b", ThreadLink: str(raw), RunID: 2, Now: fixedNow})
		if _, ok := tool.Message(err); !ok || !strings.Contains(err.Error(), "thread_link") {
			t.Errorf("thread_link %q = %v", raw, err)
		}
	}
	if list := rows(t, d); len(list) != 0 {
		t.Errorf("a rejected notification is recorded: %+v", list)
	}
	for raw, want := range map[string]string{" 7 ": "/chat/7", "/chat/12": "/chat/12", "٣": "/chat/٣"} {
		got, err := LinkFor(str(raw), 0)
		if err != nil || got != want {
			t.Errorf("LinkFor(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestPlainClipsAndStrips(t *testing.T) {
	if got := Plain("  a\tb\x1b\x7f\u00e9  ", 10); got != "a\tbé" {
		t.Errorf("Plain = %q", got)
	}
	if got := Plain(strings.Repeat("x", 130), TitleChars); len([]rune(got)) != TitleChars || !strings.HasSuffix(got, "…") {
		t.Errorf("Plain did not clip: %q", got)
	}
}

func httptestOK(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	return srv.URL
}
