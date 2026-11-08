package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func routeLines(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "flags (") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func freshImportRig(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t, loadGoldenData(t), nil, "")
	if err := os.Remove(filepath.Join(f.dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	return f, filepath.Join(golden.Dir(), "atrium.db")
}

func TestImportV1CopiesLearnedRuleData(t *testing.T) {
	t.Parallel()
	r, source := freshImportRig(t)
	code, stdout, stderr := r.Run(context.Background(), "db", "import-v1", source)
	if code != 0 {
		t.Fatalf("import-v1: exit %d stderr %q", code, stderr)
	}
	for _, want := range []string{"imported rules: 3", "imported corrections: 2", "imported decisions: 27", "imported messages: 155"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("import-v1 stdout lacks %q:\n%s", want, stdout)
		}
	}

	code, got, stderr := r.Run(context.Background(), "rules", "export")
	if code != 0 {
		t.Fatalf("rules export: exit %d stderr %q", code, stderr)
	}
	wantRules, err := os.ReadFile(filepath.Join(golden.Dir(), "cli", "import-v1", "rules-export.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(wantRules) {
		t.Errorf("rules export differs from the v1 capture:\n got %q\nwant %q", got, string(wantRules))
	}

	code, got, stderr = r.Run(context.Background(), "shadow-report")
	if code != 0 {
		t.Fatalf("shadow-report: exit %d stderr %q", code, stderr)
	}
	wantReport, err := os.ReadFile(filepath.Join(golden.Dir(), "cli", "import-v1", "shadow-report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if routeLines(got) != routeLines(string(wantReport)) {
		t.Errorf("route decisions differ from the v1 capture:\n got %q\nwant %q", routeLines(got), routeLines(string(wantReport)))
	}
}

func TestImportV1RefusesNonRE2RulePattern(t *testing.T) {
	t.Parallel()
	r, source := freshImportRig(t)
	broken := filepath.Join(t.TempDir(), "v1.db")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, data, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(broken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`UPDATE rules SET conditions = ? WHERE id = 'old-flash'`,
		`[{"field":"subject","op":"re","value":"(?=flash)sale"}]`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := r.Run(context.Background(), "db", "import-v1", broken)
	if code == 0 {
		t.Fatalf("import-v1 accepted a lookaround pattern: %s", stdout)
	}
	for _, want := range []string{"old-flash", "subject", "(?=flash)sale", "invalid or unsupported Perl syntax"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal report lacks %q:\n%s", want, stderr)
		}
	}
	d, err = db.Connect(filepath.Join(r.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, table := range []string{"rules", "messages", "corrections", "decisions"} {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s holds %d rows after a refused import", table, n)
		}
	}
}

func TestImportV1RefusesUnknownRuleField(t *testing.T) {
	t.Parallel()
	r, source := freshImportRig(t)
	broken := filepath.Join(t.TempDir(), "v1.db")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, data, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(broken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`UPDATE rules SET conditions = ? WHERE id = 'promo-spam'`,
		`[{"field":"bogus","op":"eq","value":"x"}]`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := r.Run(context.Background(), "db", "import-v1", broken)
	if code == 0 {
		t.Fatalf("import-v1 accepted an unknown field: %s", stdout)
	}
	for _, want := range []string{"promo-spam", "bogus", "unknown field"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal report lacks %q:\n%s", want, stderr)
		}
	}
	d, err = db.Connect(filepath.Join(r.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, table := range []string{"rules", "messages", "corrections", "decisions"} {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s holds %d rows after a refused import", table, n)
		}
	}
}
