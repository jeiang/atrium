package logx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var stamp = time.Unix(1760792405, 123_000_000)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	h := newTextHandler(&buf, slog.LevelInfo)
	h.loc = time.UTC
	prev := slog.Default()
	slog.SetDefault(slog.New(fixedTime{h}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

type fixedTime struct{ slog.Handler }

func (f fixedTime) Handle(ctx context.Context, r slog.Record) error {
	r.Time = stamp
	return f.Handler.Handle(ctx, r)
}

func (f fixedTime) WithAttrs(a []slog.Attr) slog.Handler { return fixedTime{f.Handler.WithAttrs(a)} }

func (f fixedTime) WithGroup(n string) slog.Handler { return fixedTime{f.Handler.WithGroup(n)} }

var (
	watchLog = Logger("atrium.watch")
	runLog   = Logger("atrium.run")
	syncLog  = Logger("atrium.sync")
)

func TestRunbookGoldens(t *testing.T) {
	tests := []struct {
		python string
		emit   func()
		golang string
	}{
		{
			"2025-10-18 13:00:05,123 atrium.watch WARNING icloud connection lost (socket closed), reconnecting in 10s",
			func() {
				watchLog.Warn(fmt.Sprintf("%s connection lost (%s), reconnecting in %ds", "icloud", "socket closed", 10), Account, "icloud")
			},
			"2025-10-18 13:00:05,123 atrium.watch WARNING icloud connection lost (socket closed), reconnecting in 10s ACCOUNT=icloud",
		},
		{
			"2025-10-18 13:00:05,123 atrium.watch ERROR gmail watch failed, retrying in 5s",
			func() {
				watchLog.Error(fmt.Sprintf("%s watch failed, retrying in %ds", "gmail", 5), Account, "gmail", Err, errors.New("division by zero"))
			},
			`2025-10-18 13:00:05,123 atrium.watch ERROR gmail watch failed, retrying in 5s ACCOUNT=gmail ERR="division by zero"`,
		},
		{
			"2025-10-18 13:00:05,123 atrium.run INFO icloud: 3 new, 1 gone, 2 flag changes",
			func() { runLog.Info(fmt.Sprintf("%s: %d new, %d gone, %d flag changes", "icloud", 3, 1, 2)) },
			"2025-10-18 13:00:05,123 atrium.run INFO icloud: 3 new, 1 gone, 2 flag changes",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run WARNING embedding unavailable: embed down",
			func() { runLog.Warn(fmt.Sprintf("embedding unavailable: %s", "embed down")) },
			"2025-10-18 13:00:05,123 atrium.run WARNING embedding unavailable: embed down",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run INFO gmail: 4 routed, 0 flag judgments",
			func() { runLog.Info(fmt.Sprintf("%s: %d routed, %d flag judgments", "gmail", 4, 0)) },
			"2025-10-18 13:00:05,123 atrium.run INFO gmail: 4 routed, 0 flag judgments",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run INFO icloud: 1 invitation proposals, 2 event candidates",
			func() { runLog.Info(fmt.Sprintf("%s: %d invitation proposals, %d event candidates", "icloud", 1, 2)) },
			"2025-10-18 13:00:05,123 atrium.run INFO icloud: 1 invitation proposals, 2 event candidates",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run ERROR icloud: event scan failed",
			func() { runLog.Error(fmt.Sprintf("%s: event scan failed", "icloud")) },
			"2025-10-18 13:00:05,123 atrium.run ERROR icloud: event scan failed",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run ERROR gmail: live pass failed",
			func() { runLog.Error(fmt.Sprintf("%s: live pass failed", "gmail")) },
			"2025-10-18 13:00:05,123 atrium.run ERROR gmail: live pass failed",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run ERROR open loops refresh failed",
			func() { runLog.Error("open loops refresh failed") },
			"2025-10-18 13:00:05,123 atrium.run ERROR open loops refresh failed",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run ERROR watches refresh failed",
			func() { runLog.Error("watches refresh failed") },
			"2025-10-18 13:00:05,123 atrium.run ERROR watches refresh failed",
		},
		{
			"2025-10-18 13:00:05,123 atrium.run ERROR money refresh failed",
			func() { runLog.Error("money refresh failed") },
			"2025-10-18 13:00:05,123 atrium.run ERROR money refresh failed",
		},
		{
			"2025-10-18 13:00:05,123 atrium.sync INFO icloud/INBOX fetched 40/57",
			func() { syncLog.Info(fmt.Sprintf("%s/%s fetched %d/%d", "icloud", "INBOX", 40, 57)) },
			"2025-10-18 13:00:05,123 atrium.sync INFO icloud/INBOX fetched 40/57",
		},
	}
	for _, tt := range tests {
		buf := capture(t)
		tt.emit()
		got := strings.TrimSuffix(buf.String(), "\n")
		if got != tt.golang {
			t.Errorf("got  %q\nwant %q", got, tt.golang)
		}
		if !strings.HasPrefix(got, tt.python) {
			t.Errorf("%q does not start with the Python line %q", got, tt.python)
		}
	}
}

func TestRootAndLevels(t *testing.T) {
	buf := capture(t)
	slog.Debug("hidden")
	slog.Info("plain")
	slog.Log(context.Background(), slog.LevelError+4, "fatal")
	want := "2025-10-18 13:00:05,123 root INFO plain\n2025-10-18 13:00:05,123 root CRITICAL fatal\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestAttributesAndRedaction(t *testing.T) {
	buf := capture(t)
	Logger("atrium.agent").With(RunID, 12).WithGroup("tool").Info("call",
		ThreadID, 7, Job, "nightly", "password", "hunter2", "NOTION_TOKEN", "secret_x",
		"api_key", "sk", "link-key", "k", "client_secret", "s", "monkey", "ok", "bytes", 30)
	want := "2025-10-18 13:00:05,123 atrium.agent INFO call RUN_ID=12 TOOL_THREAD_ID=7 TOOL_JOB=nightly " +
		"TOOL_PASSWORD=[redacted] TOOL_NOTION_TOKEN=[redacted] TOOL_API_KEY=[redacted] TOOL_LINK_KEY=[redacted] " +
		"TOOL_CLIENT_SECRET=[redacted] TOOL_MONKEY=ok TOOL_BYTES=30\n"
	if buf.String() != want {
		t.Errorf("got  %q\nwant %q", buf.String(), want)
	}
}

func TestReplaceAttrForJournal(t *testing.T) {
	tests := []struct {
		in, key, value string
	}{
		{"account", "ACCOUNT", "icloud"},
		{"run.id", "RUN_ID", "icloud"},
		{"9lives", "_9LIVES", "icloud"},
		{"GMAIL_APP_PASSWORD", "GMAIL_APP_PASSWORD", Redacted},
		{"bearerToken", "BEARERTOKEN", Redacted},
		{"TMDB_API_KEY", "TMDB_API_KEY", Redacted},
	}
	for _, tt := range tests {
		got := replaceAttr(nil, slog.String(tt.in, "icloud"))
		if got.Key != tt.key || got.Value.String() != tt.value {
			t.Errorf("replaceAttr(%q) = %s=%s, want %s=%s", tt.in, got.Key, got.Value, tt.key, tt.value)
		}
	}
}

func TestLoggerFollowsDefault(t *testing.T) {
	early := Logger("atrium.web")
	buf := capture(t)
	early.Info("started")
	if want := "2025-10-18 13:00:05,123 atrium.web INFO started\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}
