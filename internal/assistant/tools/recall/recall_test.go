package recalltools_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	atriumcontext "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/tool"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
)

func recallPage(t *testing.T, args string) py.Dict {
	t.Helper()
	d := dbtest.Schema(t)
	for _, q := range []string{
		"INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)",
		"INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)",
		"INSERT INTO tool_calls (id, run_id, tool_use_id, name, args_json, args_sha256, replay_class, state, started_at) VALUES (1, 1, 'u1', 'mail_read', '{}', 'x', 'read', 'done', 0)",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	id, _, err := atriumcontext.Store(t.Context(), d.Write, 1, 1, "mail_read", strings.Repeat("é", 20000), 0)
	if err != nil {
		t.Fatal(err)
	}
	tc := &tool.Context{DB: d, Cfg: config.Config{}, Env: config.Map(nil), Kind: tool.Chat, ThreadID: 1, RunID: 1, Now: func() float64 { return float64(time.Now().Unix()) }}
	loaded, err := py.JSONLoads([]byte(strings.Replace(args, "ID", itoa(id), 1)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := recalltools.Tools()[0].Handler(t.Context(), tc, loaded.(py.Dict))
	if err != nil {
		t.Fatal(err)
	}
	return out.(py.Dict)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func field(t *testing.T, d py.Dict, key string) any {
	t.Helper()
	v, ok := d.Get(key)
	if !ok {
		t.Fatalf("no %q in %v", key, py.JSONDumps(d))
	}
	return v
}

func TestRecallPagesDefaultToEightThousandCharacters(t *testing.T) {
	out := recallPage(t, `{"result_id": ID}`)
	if got := len([]rune(field(t, out, "text").(string))); got != recalltools.PageChars || recalltools.PageChars != 8000 {
		t.Errorf("default page = %d chars", got)
	}
	if field(t, out, "next_offset") != int64(8000) || field(t, out, "total_chars") != int64(20000) {
		t.Errorf("page = next %v total %v", field(t, out, "next_offset"), field(t, out, "total_chars"))
	}
}

func TestRecallPageSizeIsCappedAtSixteenThousand(t *testing.T) {
	out := recallPage(t, `{"result_id": ID, "limit": 50000}`)
	if got := len([]rune(field(t, out, "text").(string))); got != 16000 || recalltools.MaxPageChars != 16000 {
		t.Errorf("max page = %d chars", got)
	}
	tail := recallPage(t, `{"result_id": ID, "offset": 16000, "limit": 16000}`)
	if got := len([]rune(field(t, tail, "text").(string))); got != 4000 || field(t, tail, "next_offset") != nil {
		t.Errorf("tail = %d chars next %v", got, field(t, tail, "next_offset"))
	}
}
