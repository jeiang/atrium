package agentcmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func TestMonthlyBudgetPausesUnattendedRunsAt80AndChatAt100(t *testing.T) {
	srv := anthropictest.New(t,
		anthropictest.Reply("end_turn", anthropictest.Text("first")),
		anthropictest.Reply("end_turn", anthropictest.Text("chat at 80")),
	)
	anthropictest.PinKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	env := config.Map(map[string]string{
		"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow",
		anthropic.BaseURLEnv: srv.URL,
	})
	run := func(kind string) (int, string) {
		var stdout, stderr bytes.Buffer
		code := app(env).Run(t.Context(), []string{"agent", "run", "--text", "hello", "--kind", kind}, &stdout, &stderr)
		return code, stderr.String()
	}
	if code, stderr := run("chat"); code != cli.ExitOK {
		t.Fatalf("first chat run: exit %d: %s", code, stderr)
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	spend := func(usd float64) {
		t.Helper()
		if _, err := d.Write.Exec("INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "+
			"VALUES (?, 1, 1, 'chat', 'claude-haiku-5-5', 'medium', 0, 0, 0, 0, 0, 0, '<=100K', ?)", float64(time.Now().UnixMicro())/1e6, usd); err != nil {
			t.Fatal(err)
		}
	}

	spend(120)
	code, stderr := run("cron")
	if code != cli.ExitError || !strings.Contains(stderr, "$150.00 spent (80%); unattended runs pause at 80%") {
		t.Fatalf("cron run at 80%%: exit %d stderr %q, want a budget refusal", code, stderr)
	}
	if code, stderr := run("chat"); code != cli.ExitOK {
		t.Fatalf("chat run at 80%%: exit %d: %s", code, stderr)
	}

	spend(30)
	code, stderr = run("chat")
	if code != cli.ExitError || !strings.Contains(stderr, "(100%); chat pauses at 100%") {
		t.Fatalf("chat run at 100%%: exit %d stderr %q, want a budget refusal", code, stderr)
	}
	if n := srv.Remaining(); n != 0 {
		t.Errorf("%d scripted replies unused", n)
	}
}
