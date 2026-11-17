package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func TestAMailPassRejectsANonBooleanJudgeAnswer(t *testing.T) {
	t.Parallel()
	data := loadGoldenData(t)
	f := newFixture(t, data, nil, "")
	f.api.AnswerJSON(func(required []string, _ string) (json.RawMessage, error) {
		if slices.Contains(required, "has_event") {
			return json.RawMessage(`{"has_event":"false"}`), nil
		}
		return json.RawMessage(`{"person_asking":false,"deadline_or_payment_due":false,"security_event":false,"flag":false}`), nil
	})
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		vectors := make([]any, len(body.Input))
		for i := range body.Input {
			vector := make([]float64, config.EmbedDim)
			vector[0] = 1
			vectors[i] = map[string]any{"index": i, "embedding": vector}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": vectors})
	}))
	t.Cleanup(embed.Close)
	hostPath, _ := f.lookup("ATRIUM_HOST_CONFIG")
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigFrom(t, hostPath, f.dir, "host-judge.toml", map[string]string{
		fmt.Sprintf(`config = { url = %q, model = "embeddinggemma" }`, f.embedURL): fmt.Sprintf(`config = { url = %q, model = "embeddinggemma" }`, embed.URL+"/v1"),
	}))

	seed, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"UPDATE messages SET first_seen = (SELECT applied_at FROM schema_migrations WHERE version = 8) + 1 WHERE account = 'icloud' AND id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 3)",
		"UPDATE messages SET seen = 0, sent = 0, in_inbox = 1, gone_at = NULL WHERE account = 'icloud' AND id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 3)",
	} {
		if _, err := seed.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, _, stderr := f.runLogged(ctx, "sync", "--account", "icloud")
	if code != cli.ExitOK || !strings.Contains(stderr, "CALLER=mail_event") || !strings.Contains(stderr, "$.has_event: unexpected string") {
		t.Fatalf("sync with a non-boolean judge answer: exit %d stderr %q", code, stderr)
	}

	d, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var proposals, markers int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'calendar_event' AND json_extract(payload, '$.source.message_id') IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 3)").Scan(&proposals); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM mail_scans WHERE kind = 'events' AND found = 0").Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if proposals != 0 || markers != 0 {
		t.Fatalf("after a rejected judge answer: %d event proposals, %d zero-found event markers, want none of either until the retry", proposals, markers)
	}
}
