package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func TestAMailProposalRefusesToRunWhenTheMailModuleIsOff(t *testing.T) {
	t.Parallel()
	f := newFixture(t, loadGoldenData(t), nil, "")
	setEnv(t, "ATRIUM_CORE_MAIL_ENABLED", "false")
	code, stdout, stderr := f.Run(context.Background(), "proposals", "approve", "6")
	if code == 0 {
		t.Fatalf("approve with mail off: exit %d stdout %q", code, stdout)
	}
	if !strings.Contains(stderr, "the mail module was disabled; start the action again") {
		t.Fatalf("stderr = %q", stderr)
	}
	d, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = 6").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("proposal status = %q, want pending", status)
	}
}
