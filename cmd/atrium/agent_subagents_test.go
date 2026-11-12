package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
)

type subagentListing struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Builtin bool     `json:"builtin"`
	Effort  string   `json:"effort"`
	Tools   []string `json:"tools"`
}

func listSubagents(t *testing.T) map[string]subagentListing {
	t.Helper()
	ran := runApp(t, "agent", "subagents", "list")
	var rows []subagentListing
	if err := json.Unmarshal([]byte(ran.stdout), &rows); ran.code != cli.ExitOK || err != nil {
		t.Fatalf("list: exit %d %q: %v", ran.code, ran.stderr, err)
	}
	out := map[string]subagentListing{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

func TestAgentSubagentsCommandsManageDefinitions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seedSettings(t, map[string]string{module.FlagKey(module.Assistant): "true"})

	if mail, ok := listSubagents(t)["mail"]; !ok || !mail.Builtin || !mail.Enabled {
		t.Fatalf("built-in mail agent missing: %+v", mail)
	}

	refused := runApp(t, "agent", "subagents", "set", "evil", "--description", "d", "--prompt", "p", "--tool", "notion_create_page")
	if refused.code != cli.ExitError || !strings.Contains(refused.stderr, "not read-only tools open to subagents: notion_create_page") {
		t.Fatalf("write tool accepted: exit %d %q", refused.code, refused.stderr)
	}
	if _, ok := listSubagents(t)["evil"]; ok {
		t.Fatal("refused agent was stored")
	}

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("Use the ledger.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	made := runApp(t, "agent", "subagents", "set", "finance", "--description", "Spending and bills", "--prompt-file", promptFile, "--effort", "high", "--tool", "recall")
	if made.code != cli.ExitOK {
		t.Fatalf("set: exit %d %q", made.code, made.stderr)
	}
	if got := listSubagents(t)["finance"]; !got.Enabled || got.Builtin || got.Effort != "high" || !slices.Equal(got.Tools, []string{"recall"}) {
		t.Fatalf("finance after set: %+v", got)
	}

	if ran := runApp(t, "agent", "subagents", "disable", "finance"); ran.code != cli.ExitOK {
		t.Fatalf("disable: exit %d %q", ran.code, ran.stderr)
	}
	if ran := runApp(t, "agent", "subagents", "set", "finance", "--effort", "low"); ran.code != cli.ExitOK {
		t.Fatalf("partial update: exit %d %q", ran.code, ran.stderr)
	}
	if got := listSubagents(t)["finance"]; got.Enabled || got.Effort != "low" || !slices.Equal(got.Tools, []string{"recall"}) {
		t.Fatalf("finance after disable and update: %+v", got)
	}
	if ran := runApp(t, "agent", "subagents", "enable", "nope"); ran.code != cli.ExitError || !strings.Contains(ran.stderr, `no subagent "nope"`) {
		t.Errorf("enable unknown: exit %d %q", ran.code, ran.stderr)
	}

	builtin := runApp(t, "agent", "subagents", "delete", "mail")
	if builtin.code != cli.ExitError || !strings.Contains(builtin.stderr, "built-in agents cannot be deleted; disable instead") {
		t.Errorf("built-in delete: exit %d %q", builtin.code, builtin.stderr)
	}
	if ran := runApp(t, "agent", "subagents", "delete", "finance"); ran.code != cli.ExitOK {
		t.Fatalf("delete: exit %d %q", ran.code, ran.stderr)
	}
	listed := listSubagents(t)
	if _, ok := listed["finance"]; ok {
		t.Error("delete kept finance")
	}
	if _, ok := listed["mail"]; !ok {
		t.Error("a built-in agent was deleted")
	}
}
