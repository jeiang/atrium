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
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			ResponseFormat struct {
				JSONSchema struct {
					Schema struct {
						Required []string `json:"required"`
					} `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		answer := `{"person_asking":false,"deadline_or_payment_due":false,"security_event":false,"flag":false}`
		switch required := body.ResponseFormat.JSONSchema.Schema.Required; {
		case slices.Contains(required, "folder"):
			answer = `{"folder":"uncertain","confident":false}`
		case slices.Contains(required, "has_event"):
			answer = `{"has_event":"false"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
	}))
	t.Cleanup(chat.Close)
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
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", chat.URL+"/v1")
	hostPath, _ := f.lookup("ATRIUM_HOST_CONFIG")
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigFrom(t, hostPath, f.dir, "host-judge.toml", map[string]string{
		fmt.Sprintf(`config = { url = %q, model = "embeddinggemma" }`, f.embedURL): fmt.Sprintf(`config = { url = %q, model = "embeddinggemma" }`, embed.URL+"/v1"),
	}))

	seed, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DELETE FROM event_candidates",
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
	code, _, stderr := f.runLogged(ctx, "watch", "--account", "icloud")
	if code != cli.ExitOK || !strings.Contains(stderr, "event scan failed") || !strings.Contains(stderr, "has_event") {
		t.Fatalf("watch with a non-boolean judge answer: exit %d stderr %q", code, stderr)
	}

	d, err := db.Connect(filepath.Join(f.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var candidates int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM event_candidates").Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("%d event candidates after a rejected judge answer", candidates)
	}
}
