package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestRemovedOpsCondenserInputIsRefusedAtStartup(t *testing.T) {
	for _, name := range []string{"ATRIUM_ASSISTANT_OPS_CONDENSER", "ATRIUM_ASSISTANT_OPS_CONDENSER_MODEL", "ATRIUM_ASSISTANT_OPS_CONDENSER_MAX_TOKENS"} {
		t.Run(name, func(t *testing.T) {
			moduleFixture(t, flagsExcept("", ""))
			setEnv(t, name, "1")
			ran := runApp(t, "settings", "list")
			if ran.code == cli.ExitOK || !strings.Contains(ran.stderr, name) || !strings.Contains(ran.stderr, "ops_worker_monthly_usd") {
				t.Errorf("%s: exit %d stderr %q, want a refusal naming the replacement", name, ran.code, ran.stderr)
			}
		})
	}
	t.Run("host config", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, flagsExcept("", ""))
		raw, err := os.ReadFile(defaultHostConfig)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "host-ops.toml")
		if err := os.WriteFile(path, append(raw, []byte("\n[assistant]\nops_condenser = \"local\"\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		setEnv(t, "ATRIUM_HOST_CONFIG", path)
		ran := runApp(t, "settings", "list")
		if ran.code == cli.ExitOK || !strings.Contains(ran.stderr, "assistant.ops_condenser") || !strings.Contains(ran.stderr, "ops_worker_monthly_usd") {
			t.Errorf("host assistant.ops_condenser: exit %d stderr %q, want a refusal naming the replacement", ran.code, ran.stderr)
		}
	})
}
