package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func TestResolvedNotionContentAdvancesTheMapping(t *testing.T) {
	fake := &exportNotion{}
	_, f, d := exportFixture(t, fake)
	exportEnable(t, "messages,personal")
	id := exportRecord(t, d, "Dentist", "first body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	pageID := fake.first()
	if pageID == "" {
		t.Fatal("the first export created no page")
	}
	if _, err := d.Write.Exec("UPDATE kb_records SET body = 'second body', updated_at = 300 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	fake.dropMarkdown = true
	runExport(t, f, d)
	var effectID int64
	var action string
	if err := d.Read.QueryRow("SELECT id, action_json FROM effects WHERE operation = 'kb_export_content' AND state = 'unknown' ORDER BY id DESC LIMIT 1").
		Scan(&effectID, &action); err != nil {
		t.Fatalf("unknown content: %v", err)
	}
	var payload struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal([]byte(action), &payload); err != nil || payload.Hash == "" {
		t.Fatalf("content action = %q, %v", action, err)
	}
	if ran := runApp(t, "effects", "resolve", fmt.Sprint(effectID), "--outcome", "applied"); ran.code != cli.ExitOK {
		t.Fatalf("resolve: %d %s", ran.code, ran.stderr)
	}
	var hash string
	if err := d.Read.QueryRow("SELECT hash FROM export_pages WHERE target = 'notion' AND record_id = ?", id).Scan(&hash); err != nil {
		t.Fatalf("mapping after the resolution: %v", err)
	}
	if hash != payload.Hash {
		t.Fatalf("mapping hash = %q, want %q", hash, payload.Hash)
	}
	before := countRequests(fake, "PATCH /v1/pages/"+pageID+"/markdown")
	runExport(t, f, d)
	if got := countRequests(fake, "PATCH /v1/pages/"+pageID+"/markdown"); got != before {
		t.Errorf("the export re-sent the resolved content (%d -> %d)", before, got)
	}
	page := fake.byTitle("Dentist")
	if page == nil || !strings.Contains(page.markdown, "second body") {
		t.Fatalf("page after the resolution = %+v", page)
	}
}

func TestResolvedNotionTitleLeavesTheContentPending(t *testing.T) {
	fake := &exportNotion{}
	_, f, d := exportFixture(t, fake)
	exportEnable(t, "messages,personal")
	id := exportRecord(t, d, "Dentist", "first body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	pageID := fake.first()
	if pageID == "" {
		t.Fatal("the first export created no page")
	}
	if _, err := d.Write.Exec("UPDATE kb_records SET title = 'Dentist visit', body = 'second body', updated_at = 300 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	fake.dropTitle = true
	runExport(t, f, d)
	var effectID int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'kb_export_title' AND state = 'unknown' ORDER BY id DESC LIMIT 1").
		Scan(&effectID); err != nil {
		t.Fatalf("unknown title: %v", err)
	}
	if ran := runApp(t, "effects", "resolve", fmt.Sprint(effectID), "--outcome", "applied"); ran.code != cli.ExitOK {
		t.Fatalf("resolve: %d %s", ran.code, ran.stderr)
	}
	before := countRequests(fake, "PATCH /v1/pages/"+pageID)
	runExport(t, f, d)
	if got := countRequests(fake, "PATCH /v1/pages/"+pageID); got != before {
		t.Errorf("the export re-sent the resolved title (%d -> %d)", before, got)
	}
	page := fake.byTitle("Dentist visit")
	if page == nil || !strings.Contains(page.markdown, "second body") {
		t.Fatalf("page after the title resolution = %+v", page)
	}
}

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

func TestNotionRecreatesAfterTheMappingWasTrashed(t *testing.T) {
	fake := &exportNotion{}
	_, f, d := exportFixture(t, fake)
	exportEnable(t, "messages,personal")
	id := exportRecord(t, d, "Dentist", "body", "personal", "chat", 0, 0, 0, 0, 0)
	runExport(t, f, d)
	if page := fake.byTitle("Dentist"); page == nil || page.trashed {
		t.Fatalf("the first export page = %+v", page)
	}
	if _, err := d.Write.Exec("UPDATE kb_records SET retracted_at = 300 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	if page := fake.byTitle("Dentist"); page == nil || !page.trashed {
		t.Fatalf("the retraction did not trash the page: %+v", page)
	}
	if _, err := d.Write.Exec("UPDATE kb_records SET retracted_at = NULL, updated_at = 400 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	runExport(t, f, d)
	var live, total int
	for _, page := range fake.created() {
		if page.title == "Dentist" {
			total++
			if !page.trashed {
				live++
			}
		}
	}
	if live != 1 || total != 2 {
		t.Fatalf("Dentist pages after the restore = %d live of %d, want 1 of 2", live, total)
	}
}
