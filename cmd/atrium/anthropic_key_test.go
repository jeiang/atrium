//go:build !modernc

package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func waitForLog(t *testing.T, logs *lockedBuffer, want string) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if strings.Contains(logs.String(), want) {
			return
		}
	}
	t.Fatalf("logs never contained %q: %q", want, logs.String())
}

func TestStartupWarnsForACatalogModelTheModelsAPIOmits(t *testing.T) {
	srv := anthropictest.New(t)
	srv.Models = []string{"claude-haiku-5-5", "claude-sonnet-5-5"}
	dir, _, _ := moduleFixture(t, flagsExcept("assistant", "true"))
	setEnv(t, anthropic.BaseURLEnv, srv.URL)
	setEnv(t, "ANTHROPIC_API_KEY", anthropictest.APIKey)
	base, logs := startServeLogged(t, dir)
	waitForLog(t, logs, "claude-opus-5-5")
	text := logs.String()
	if n := strings.Count(text, "not offered by the Anthropic Models API"); n != 1 || strings.Contains(text, anthropictest.APIKey) {
		t.Errorf("logs %q", text)
	}
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz %d, want the server up despite the warning", resp.StatusCode)
	}
}

func TestStartupWarnsWhenTheModelsAPIFails(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("assistant", "true"))
	setEnv(t, anthropic.BaseURLEnv, "http://127.0.0.1:9")
	_, logs := startServeLogged(t, dir)
	waitForLog(t, logs, "model catalog is unchecked")
}
