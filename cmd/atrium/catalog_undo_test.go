package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/mailtest"
)

func TestCatalogMailExecutorUndoCLIWebParity(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		paused     bool
		state      string
		writes     int
	}{
		{"live", "live", false, "done", 2},
		{"paused", "live", true, "done", 2},
		{"shadow", "shadow", false, "failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mrvMailFixture(t, tc.mode)
			m.fake.AddBox("UndoCLI", mailtest.NewBox(20))
			m.fake.AddBox("UndoWeb", mailtest.NewBox(21))
			if tc.paused {
				if got := runApp(t, "live", "pause"); got.code != cli.ExitOK {
					t.Fatalf("pause: %d %s", got.code, got.stderr)
				}
			}
			d := mustConnect(t, filepath.Join(m.dir, "atrium.db"), db.Key{})
			defer func() { _ = d.Close() }()
			now := float64(time.Now().Unix())
			seed := func(name string) string {
				res, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, account, target, action_json, undo_json, state, settled_at) VALUES (?, ?, ?, 'proposal', 'create_folder', ?, 'forward', 'icloud', ?, '{}', ?, 'done', ?)", now, now, name, name, name, fmt.Sprintf(`{"op":"delete_folder","account":"icloud","imap_name":%q}`, name), now)
				if err != nil {
					t.Fatal(err)
				}
				id, err := res.LastInsertId()
				if err != nil {
					t.Fatal(err)
				}
				return fmt.Sprint(id)
			}
			cliID, webID := seed("UndoCLI"), seed("UndoWeb")
			s := startAPISite(t, m.dir, nil)
			owner := s.token("owner")
			got := runApp(t, "effects", "undo", cliID)
			if got.code != cli.ExitOK {
				t.Fatalf("CLI undo: %d %s", got.code, got.stderr)
			}
			web := s.do(http.MethodPost, "/api/v1/effects/"+webID+"/undo", owner, nil)
			if web.status != http.StatusOK {
				t.Fatalf("web undo: %d %s", web.status, web.body)
			}
			for _, id := range []string{cliID, webID} {
				var state string
				if err := d.Read.QueryRow("SELECT state FROM effects WHERE undo_of = ?", id).Scan(&state); err != nil || state != tc.state {
					t.Fatalf("undo %s state %q: %v", id, state, err)
				}
			}
			if len(m.fake.Writes) != tc.writes {
				t.Fatalf("IMAP writes %v, want %d", m.fake.Writes, tc.writes)
			}
			if tc.state == "done" {
				before := len(m.fake.Writes)
				if got := runApp(t, "effects", "undo", cliID); got.code == cli.ExitOK {
					t.Fatalf("repeat CLI undo succeeded")
				}
				again := s.do(http.MethodPost, "/api/v1/effects/"+webID+"/undo", owner, nil)
				if again.status != http.StatusConflict {
					t.Fatalf("repeat web undo: %d %s", again.status, again.body)
				}
				if len(m.fake.Writes) != before {
					t.Fatalf("refused undo wrote to IMAP: %v", m.fake.Writes[before:])
				}
			}
		})
	}
}

func TestCatalogMediaUndoThroughCLI(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/add", url.Values{"magnet": {mediaMagnet(hash, "Catalog.Undo")}, "mode": {"media"}}, http.StatusOK)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	id := mediaEffectID(t, d, "media_add_magnet", hash, "done")
	if got := runApp(t, "effects", "undo", fmt.Sprint(id)); got.code != cli.ExitOK {
		t.Fatalf("media undo: %d %s", got.code, got.stderr)
	}
	if qb.count("delete") != 1 {
		t.Fatalf("media undo made %d deletes", qb.count("delete"))
	}
	var state string
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE undo_of = ?", id).Scan(&state); err != nil || state != "done" {
		t.Fatalf("media undo state %q: %v", state, err)
	}
}

func TestCatalogProposeHidesDisabledMailTriage(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("mail", "false"))
	start := warm(t, api)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p1", "propose", map[string]any{"kind": "mail_triage", "payload": map[string]any{"items": []any{}}})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp(t, "agent", "run", "--text", "triage")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent: %d %s", ran.code, ran.stderr)
	}
	reqs := api.Requests()[start:]
	if len(reqs) < 2 {
		t.Fatalf("expected a tool response, got %d requests", len(reqs))
	}
	first := reqs[0].JSON(t)
	tools, _ := first["tools"].([]any)
	found := false
	for _, raw := range tools {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "propose" {
			found = true
			b, _ := json.Marshal(entry)
			if strings.Contains(string(b), "mail_triage") {
				t.Fatalf("disabled kind offered: %s", b)
			}
		}
	}
	if !found {
		t.Fatal("propose tool not offered")
	}
	b, _ := json.Marshal(reqs[1].JSON(t))
	if !strings.Contains(string(b), `"is_error":true`) {
		t.Fatalf("tool refusal missing: %s", b)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'mail_triage'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("mail triage proposals = %d: %v", n, err)
	}
}
