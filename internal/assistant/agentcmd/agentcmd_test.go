package agentcmd

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
)

func app(env config.Env) *cli.App {
	a := cli.New("atrium", func(io.Writer) {})
	a.Add(&cli.Command{Name: "agent", Commands: []*cli.Command{runCommand(env)}})
	return a
}

func TestRunPrintsTheRunView(t *testing.T) {
	srv := anthropictest.New(t, anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow",
		"ANTHROPIC_API_KEY": anthropictest.APIKey, anthropic.BaseURLEnv: srv.URL,
	})
	var stdout, stderr bytes.Buffer
	if code := app(env).Run(t.Context(), []string{"agent", "run", "--text", "hello", "--kind", "cron"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "{\n  \"id\": 1,\n  \"thread_id\": 1,") {
		t.Errorf("output %q", stdout.String())
	}
	var view map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["state"] != "done" || view["kind"] != "cron" || view["calls"] == nil || view["cost"].(float64) <= 0 || view["skills"] == nil {
		t.Errorf("view %v", view)
	}
	if n := srv.Remaining(); n != 0 {
		t.Errorf("%d scripted replies unused", n)
	}
}

func TestRunRequiresTextAndAKnownKind(t *testing.T) {
	env := config.Map(map[string]string{})
	for _, args := range [][]string{{"agent", "run"}, {"agent", "run", "--text", "x", "--kind", "code"}} {
		var stdout, stderr bytes.Buffer
		if code := app(env).Run(t.Context(), args, &stdout, &stderr); code != cli.ExitUsage {
			t.Errorf("%v: exit %d (%s)", args, code, stderr.String())
		}
	}
}
