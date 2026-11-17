package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func TestARefusedBackgroundRequestHoldsAcrossARestartUntilASuccess(t *testing.T) {
	t.Parallel()
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(unit.Mail, ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", ""))
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("passing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(unit.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("enabling mail: %d %q", on.code, on.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'INBOX', 'INBOX', 'inbox')"); err != nil {
		t.Fatal(err)
	}
	dbtest.AddMessage(t, d, 1, "a message to embed", map[string]any{"subject": "Refused", "from_addr": "seed@x.test"})
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO embed_index (generation, state, url, model, dimension) VALUES (0, 'active', '', 'embeddinggemma', ?)", config.EmbedDim); err != nil {
		t.Fatal(err)
	}
	storedHold := func() bool {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(`SELECT COUNT(*) FROM connections, json_each(results_json)
			WHERE id = 'service/embedding' AND json_extract(value, '$.capability') = 'embeddings'
			AND json_extract(value, '$.data.backoff_until') IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	const paused = "embedding index paused after a refusal"
	sentEmbeddings := func() int { return len(embed.seen("/v1/embeddings")) }

	embed.setStatus(http.StatusTooManyRequests)
	_, logs, stop := startServeWith(t, dir, true)
	waitUntil(t, time.Minute, "the refused embedding index to pause and store its hold", func() bool {
		return strings.Contains(logs.String(), paused) && storedHold()
	})
	sentBeforeRestart := sentEmbeddings()
	stop()

	_, logs, stop = startServeWith(t, dir, true)
	waitUntil(t, time.Minute, "the restarted embedding index to wait out the stored hold", func() bool {
		return strings.Contains(logs.String(), paused)
	})
	if sent := sentEmbeddings(); sent != sentBeforeRestart {
		t.Fatalf("the held embedding index sent %d embedding requests after the restart, want none", sent-sentBeforeRestart)
	}
	stop()

	embed.setStatus(0)
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("passing Test after the refusals: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if storedHold() {
		t.Fatal("a passing Test left the refusal hold stored")
	}
	_, logs, _ = startServeWith(t, dir, true)
	waitUntil(t, time.Minute, "the embedding index to send again after the hold cleared", func() bool {
		return sentEmbeddings() > sentBeforeRestart
	})
	if strings.Contains(logs.String(), paused) {
		t.Fatal("the embedding index paused after its hold was cleared")
	}
}
