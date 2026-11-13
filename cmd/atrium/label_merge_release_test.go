package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
)

func TestLabelMergeReleasesTheDeletionWhenTheFolderBecameNonEmpty(t *testing.T) {
	f := newFixture(t, loadGoldenData(t), nil, "")
	if code, _, stderr := f.Run(context.Background(), "gmail-labels", "scan"); code != cli.ExitOK {
		t.Fatalf("gmail-labels scan: exit %d stderr %q", code, stderr)
	}
	d := mustConnect(t, filepath.Join(f.dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var pid int64
	var payload string
	if err := d.Read.QueryRow("SELECT id, payload FROM proposals WHERE kind = 'gmail_label_merge' AND status = 'pending' ORDER BY id LIMIT 1").
		Scan(&pid, &payload); err != nil {
		t.Fatalf("label merge proposal: %v", err)
	}
	var plan struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal([]byte(payload), &plan); err != nil || plan.Label == "" {
		t.Fatalf("proposal payload = %q, %v", payload, err)
	}
	label := mail.UTF7Encode(plan.Label)
	calls := 0
	f.fakes["gmail"].SetStatusHook(func(name string, messages uint32) uint32 {
		if name != label {
			return messages
		}
		calls++
		if calls >= 2 {
			return 1
		}
		return messages
	})
	if code, _, stderr := f.Run(context.Background(), "proposals", "approve", strconv.FormatInt(pid, 10)); code != cli.ExitOK {
		t.Fatalf("proposals approve: exit %d stderr %q", code, stderr)
	}
	var state string
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE proposal_id = ? AND action_json LIKE '%delete_label%' ORDER BY id DESC LIMIT 1", pid).
		Scan(&state); err != nil {
		t.Fatalf("label deletion effect: %v", err)
	}
	if state != "skipped" {
		t.Fatalf("label deletion effect = %q, want skipped", state)
	}
	var holding int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effect_resources er JOIN effects e ON e.id = er.effect_id "+
		"WHERE e.proposal_id = ? AND er.holding = 1 AND e.action_json LIKE '%delete_label%'", pid).Scan(&holding); err != nil {
		t.Fatal(err)
	}
	if holding != 0 {
		t.Fatalf("%d claims still held by the skipped deletion", holding)
	}
	var result string
	if err := d.Read.QueryRow("SELECT COALESCE(result, '') FROM proposals WHERE id = ?", pid).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "kept") {
		t.Fatalf("proposal result = %q, want the kept label", result)
	}
}
