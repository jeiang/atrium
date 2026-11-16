package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func TestResolvedObsidianWriteAdvancesTheMapping(t *testing.T) {
	_, f, d := obsidianFixture(t)
	vault := t.TempDir()
	obsidianEnable(t, vault)
	id := exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	rel := "personal/dentist-K" + fmt.Sprint(id) + ".md"
	if _, err := os.Stat(filepath.Join(vault, rel)); err != nil {
		t.Fatalf("first export: %v", err)
	}
	var hash string
	if err := d.Read.QueryRow("SELECT hash FROM export_pages WHERE target = 'obsidian' AND record_id = ?", id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("DELETE FROM export_pages WHERE target = 'obsidian' AND record_id = ?", id); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	effectID := seedEffect(t, d, seededEffect{
		Origin: "export", Operation: "obsidian_write", OperationKey: "export:obsidian:K" + fmt.Sprint(id) + ":0:write:" + rel + ":lost",
		Direction: "forward", Target: "obsidian:" + rel,
		Action: fmt.Sprintf(`{"hash":%q,"path":%q,"record_id":%d,"mapping":true}`, hash, rel, id),
		State:  "unknown", CreatedAt: now, LeaseAt: now, Settled: true,
	})
	if ran := runApp(t, "effects", "resolve", fmt.Sprint(effectID), "--outcome", "applied"); ran.code != cli.ExitOK {
		t.Fatalf("resolve: %d %s", ran.code, ran.stderr)
	}
	var restored string
	if err := d.Read.QueryRow("SELECT hash FROM export_pages WHERE target = 'obsidian' AND record_id = ?", id).Scan(&restored); err != nil {
		t.Fatalf("mapping after the resolution: %v", err)
	}
	if restored != hash {
		t.Fatalf("mapping hash = %q, want %q", restored, hash)
	}
	var before int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE operation = 'obsidian_write'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	var after int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE operation = 'obsidian_write'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("the export rewrote the resolved file (%d -> %d writes)", before, after)
	}
}

func TestObsidianRepeatedRenameRemovesTheOldPath(t *testing.T) {
	_, f, d := obsidianFixture(t)
	vault := t.TempDir()
	obsidianEnable(t, vault)
	id := exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	a := "personal/dentist-K" + fmt.Sprint(id) + ".md"
	b := "work/dentist-appointment-K" + fmt.Sprint(id) + ".md"
	rename := func(title, scope string, updated float64) {
		t.Helper()
		if _, err := d.Write.Exec("UPDATE kb_records SET title = ?, scope = ?, updated_at = ? WHERE id = ?", title, scope, updated, id); err != nil {
			t.Fatal(err)
		}
		runExport(t, f, d)
	}
	rename("Dentist appointment", "work", 300)
	if _, err := os.Stat(filepath.Join(vault, a)); !os.IsNotExist(err) {
		t.Fatalf("the first rename left %s: %v", a, err)
	}
	rename("Dentist", "personal", 400)
	if _, err := os.Stat(filepath.Join(vault, b)); !os.IsNotExist(err) {
		t.Fatalf("the second rename left %s: %v", b, err)
	}
	rename("Dentist appointment", "work", 500)
	if _, err := os.Stat(filepath.Join(vault, a)); !os.IsNotExist(err) {
		t.Fatalf("the third rename left %s: %v", a, err)
	}
	if _, err := os.Stat(filepath.Join(vault, b)); err != nil {
		t.Fatalf("the third rename did not write %s: %v", b, err)
	}
	var remote string
	if err := d.Read.QueryRow("SELECT remote_id FROM export_pages WHERE target = 'obsidian' AND record_id = ?", id).Scan(&remote); err != nil {
		t.Fatal(err)
	}
	if remote != b {
		t.Fatalf("mapping remote id = %q, want %q", remote, b)
	}
}
