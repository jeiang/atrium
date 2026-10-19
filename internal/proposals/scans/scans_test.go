package scans_test

import (
	"context"
	"testing"

	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/proposals/scans"
	"github.com/jeiang/atrium/internal/py"
)

func TestScanAllRunsEveryScanner(t *testing.T) {
	d := dbtest.Schema(t)
	out, err := scans.ScanAll(context.Background(), d, 1_792_080_000, func(context.Context, string, []string, []string) (string, error) {
		t.Fatal("nothing to name")
		return "", nil
	})
	want := `{"rebalance": {"candidates": 0, "created": 0}, "deletions": {"candidates": 0, "created": 0}, "gmail_labels": {"candidates": 0, "created": 0}}`
	if err != nil || py.JSONDumps(out) != want {
		t.Fatalf("ScanAll = %s, %v", py.JSONDumps(out), err)
	}
}
