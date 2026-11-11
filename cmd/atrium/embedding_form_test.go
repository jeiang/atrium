package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddingConnectionFormKeepsTheStoredModel(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	embed := newFakeOpenAI(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, embed.URL+"/v1", "custom-model", ""))

	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/connections", nil); code != http.StatusOK {
		t.Fatalf("connections page: %d", code)
	}
	form := url.Values{"id": {"service/embedding"}, "url": {embed.URL + "/v1"}}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/connections/test", form); code != http.StatusOK {
		t.Fatalf("submitting the embedding form = %d: %.300s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
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
