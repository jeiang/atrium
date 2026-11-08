package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/secrets"
)

func TestStoredSecretsAreRedactedFromLogsAndAgentWrites(t *testing.T) {
	const secret = "sk-redact-profile-é-0123456789"
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	setCmd := cliExitProcess(t, "secrets set "+secrets.ActionLinkKey)
	setCmd.Stdin = strings.NewReader(secret + "\n")
	var setErr bytes.Buffer
	setCmd.Stderr = &setErr
	if err := setCmd.Run(); err != nil {
		t.Fatalf("secrets set subprocess: %v, stderr %q", err, setErr.String())
	}

	api := anthropictest.New(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "profile_set", map[string]any{"key": "note", "value": "token " + secret})),
		anthropictest.Reply("end_turn", anthropictest.Text("saved")),
	)
	t.Setenv(anthropic.BaseURLEnv, api.URL)
	ran := runApp("agent", "run", "--text", "remember my token", "--kind", "chat")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	if strings.Contains(ran.stderr, secret) {
		t.Errorf("the secret reached the log stream: %q", ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var after string
	if err := d.Read.QueryRow("SELECT after_json FROM agent_writes WHERE tool = 'profile_set'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, secret) || !strings.Contains(after, logx.Redacted) {
		t.Errorf("agent_writes snapshot = %q, want the secret replaced by %s", after, logx.Redacted)
	}
}
