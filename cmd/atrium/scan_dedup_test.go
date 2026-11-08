package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestASecondScanDoesNotDuplicateAProposal(t *testing.T) {
	f := newFixture(t, loadGoldenData(t), nil, "")
	path := filepath.Join(f.dir, "atrium.db")
	count := func() int {
		d, err := db.Connect(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Close() }()
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'gmail_label_merge'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	code, _, stderr := f.Run(context.Background(), "gmail-labels", "scan")
	if code != cli.ExitOK {
		t.Fatalf("first scan: exit %d stderr %q", code, stderr)
	}
	first := count()
	code, stdout, stderr := f.Run(context.Background(), "gmail-labels", "scan")
	if code != cli.ExitOK {
		t.Fatalf("second scan: exit %d stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, `"created": 0`) {
		t.Errorf("second scan stdout = %q, want no new proposal", stdout)
	}
	if second := count(); second != first {
		t.Errorf("proposal count = %d after the second scan, want %d", second, first)
	}
}
