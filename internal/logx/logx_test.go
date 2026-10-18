package logx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
		ThreadID, 7, Job, "nightly", "password", "fake-value", "NOTION_TOKEN", "fake-value",
		"api_key", "fake-value", "link-key", "fake-value", "client_secret", "fake-value",
		"input_tokens", 1200, "monkey", "ok", "bytes", 30)
	want := "2025-10-18 13:00:05,123 atrium.agent INFO call RUN_ID=12 TOOL_THREAD_ID=7 TOOL_JOB=nightly " +
		"TOOL_PASSWORD=[redacted] TOOL_NOTION_TOKEN=[redacted] TOOL_API_KEY=[redacted] TOOL_LINK_KEY=[redacted] " +
		"TOOL_CLIENT_SECRET=[redacted] TOOL_INPUT_TOKENS=1200 TOOL_MONKEY=ok TOOL_BYTES=30\n"
	if buf.String() != want {
		t.Errorf("got  %q\nwant %q", buf.String(), want)
	}
}

func TestIsSecret(t *testing.T) {
	tests := map[string]bool{
		"password":           true,
		"GMAIL_APP_PASSWORD": true,
		"passwd":             true,
		"client_secret":      true,
		"NOTION_TOKEN":       true,
		"token":              true,
		"token_value":        true,
		"bearerToken":        true,
		"refresh-token":      true,
		"api_key":            true,
		"apikey":             true,
		"TMDB_API_KEY":       true,
		"key":                true,
		"private_key":        true,
		"privateKey":         true,
		"authorization":      true,
		"cookie":             true,
		"set_cookies":        true,
		"credential":         true,
		"credentials":        true,
		"input_tokens":       false,
		"output_tokens":      false,
		"cache_read_tokens":  false,
		"tokens_used":        false,
		"TOKENS":             false,
		"monkey":             false,
		"keys":               false,
		"ACCOUNT":            false,
		"RUN_ID":             false,
		"bytes":              false,
		"message_count":      false,
		"COMPONENT":          false,
	}
	for name, want := range tests {
		if got := IsSecret(name); got != want {
			t.Errorf("IsSecret(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestKey(t *testing.T) {
	tests := map[string]string{
		"account":           "ACCOUNT",
		"run.id":            "RUN_ID",
		"9lives":            "F_9LIVES",
		"_hidden":           "F_HIDDEN",
		"-x":                "F_X",
		"":                  "F_",
		"message":           "F_MESSAGE",
		"MESSAGE_ID":        "F_MESSAGE_ID",
		"priority":          "F_PRIORITY",
		"code_file":         "F_CODE_FILE",
		"syslog_identifier": "F_SYSLOG_IDENTIFIER",
		"errno":             "F_ERRNO",
		"message_count":     "MESSAGE_COUNT",
		"COMPONENT":         "COMPONENT",
	}
	for in, want := range tests {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

type groupValuer struct{}

func (groupValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("value", "S1-fake"), slog.Int("len", 7))
}

var bypasses = []struct {
	name string
	emit func(l *slog.Logger)
	want []string
}{
	{"group named token", func(l *slog.Logger) { l.Info("m", slog.Group("token", "value", "S1-fake")) }, []string{"TOKEN=[redacted]"}},
	{"WithGroup password", func(l *slog.Logger) { l.WithGroup("password").Info("m", "value", "S1-fake", "len", 7) }, []string{"PASSWORD_VALUE=[redacted]", "PASSWORD_LEN=[redacted]"}},
	{"LogValuer group under token", func(l *slog.Logger) { l.Info("m", "token", groupValuer{}) }, []string{"TOKEN=[redacted]"}},
	{"nested group under secret group", func(l *slog.Logger) {
		l.Info("m", slog.Group("auth", slog.Group("cookie", "session", "S1-fake")))
	}, []string{"AUTH_COOKIE=[redacted]"}},
	{"With group named credentials", func(l *slog.Logger) { l.With(slog.Group("credentials", "user", "S1-fake")).Info("m") }, []string{"CREDENTIALS=[redacted]"}},
	{"message collision", func(l *slog.Logger) { l.Info("m", "message", "body-size", "priority", 3) }, []string{"F_MESSAGE=body-size", "F_PRIORITY=3"}},
}

func TestTextRedactsGroups(t *testing.T) {
	for _, tt := range bypasses {
		t.Run(tt.name, func(t *testing.T) {
			buf := capture(t)
			tt.emit(slog.Default())
			got := buf.String()
			if strings.Contains(got, "S1-fake") {
				t.Errorf("secret leaked: %q", got)
			}
			if want := "2025-10-18 13:00:05,123 root INFO m " + strings.Join(tt.want, " ") + "\n"; got != want {
				t.Errorf("got  %q\nwant %q", got, want)
			}
		})
	}
}

type recorder struct {
	pre []slog.Attr
	out *[]string
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := slices.Clone(r.pre)
	rec.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	for _, a := range attrs {
		if a.Value.Kind() == slog.KindGroup {
			return fmt.Errorf("group %q reached the journal handler", a.Key)
		}
		*r.out = append(*r.out, a.Key+"="+a.Value.String())
	}
	return nil
}

func (r *recorder) WithAttrs(as []slog.Attr) slog.Handler {
	return &recorder{pre: append(slices.Clip(r.pre), as...), out: r.out}
}

func (r *recorder) WithGroup(string) slog.Handler {
	panic("flatHandler forwarded a group to the journal handler")
}

func TestJournalRedactsGroups(t *testing.T) {
	for _, tt := range bypasses {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			tt.emit(slog.New(&flatHandler{inner: &recorder{out: &got}}))
			if !slices.Equal(got, tt.want) {
				t.Errorf("fields = %q, want %q", got, tt.want)
			}
		})
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
