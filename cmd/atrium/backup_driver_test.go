package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestPlaintextBackupVerifiesAndRestoresUnderEitherDriver(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	if got := runApp("settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list exited %d: %s", got.code, got.stderr)
	}
	archive := filepath.Join(dir, "plain.backup")
	if got := runApp("backup", archive); got.code != cli.ExitOK || !strings.Contains(got.stdout, "plaintext") {
		t.Fatalf("backup exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if got := runApp("restore", "--verify", archive); got.code != cli.ExitOK || !strings.Contains(got.stdout, "integrity_check: ok") {
		t.Fatalf("verify exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if got := runApp("restore", archive); got.code != cli.ExitOK {
		t.Fatalf("restore exited %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if got := runApp("settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list after restore exited %d: %s", got.code, got.stderr)
	}
}
