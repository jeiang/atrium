package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func TestEmbeddingConnectionFormKeepsTheStoredModel(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	embed := newFakeOpenAI(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, embed.URL+"/v1", "custom-model", ""))

	base := startServeIn(t, dir)
	browser := webG3(t, base)
	okG3(t, "testing and saving the embedding connection", browser.do(http.MethodPut, "/web/v1/connections/service/embedding", map[string]any{"url": embed.URL + "/v1"}))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	var configJSON string
	if err := d.Read.QueryRow("SELECT config_json FROM connections WHERE id = 'service/embedding'").Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configJSON, `"model":"custom-model"`) {
		t.Fatalf("the embedding config after its form was submitted is %s", configJSON)
	}
	if state := connectionState(t, dir, "service/embedding"); state != "ok" {
		t.Fatalf("the embedding connection after its form was submitted is %s, want ok", state)
	}
}
