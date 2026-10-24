//go:build !modernc

package main

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func runWithInput(t *testing.T, stdin, args string, env ...string) cliRun {
	t.Helper()
	cmd := cliExitProcess(t, args)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cliRun{exitCode(cmd.Run()), stdout.String(), stderr.String()}
}

func waitForLog(t *testing.T, logs *lockedBuffer, want string) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if strings.Contains(logs.String(), want) {
			return
		}
	}
	t.Fatalf("logs never contained %q: %q", want, logs.String())
}

func TestAnthropicKeyIsAWriteOnlySecretThatPinsOverride(t *testing.T) {
	const stored = "sk-ant-stored-0123456789-never-printed"
	const pinned = "sk-ant-pinned-0123456789-never-printed"
	dir := t.TempDir()
	srv := anthropictest.New(t,
		anthropictest.Reply("end_turn", anthropictest.Text("from the stored key")),
		anthropictest.Reply("end_turn", anthropictest.Text("from the pin")),
	)
	base := []string{
		"ATRIUM_CACHE_DIR=" + filepath.Join(dir, "cache"), "ATRIUM_CONFIG=",
		"ANTHROPIC_API_KEY=", "ATRIUM_ANTHROPIC_ANTHROPIC_API_KEY=", "CREDENTIALS_DIRECTORY=", anthropic.BaseURLEnv + "=" + srv.URL,
	}
	plain := append([]string{"ATRIUM_DB=" + filepath.Join(dir, "plain.db"), "ATRIUM_DB_KEY="}, base...)
	encrypted := append([]string{"ATRIUM_DB=" + filepath.Join(dir, "encrypted.db"), "ATRIUM_DB_KEY=" + rightKeyHex}, base...)

	refused := runWithInput(t, stored+"\n", "secrets set anthropic_api_key", plain...)
	if refused.code != cli.ExitError || !strings.Contains(refused.stderr, "encrypt the database first") || strings.Contains(refused.stdout+refused.stderr, stored) {
		t.Fatalf("set on a plaintext database: exit %d stdout %q stderr %q", refused.code, refused.stdout, refused.stderr)
	}

	set := runWithInput(t, stored+"\n", "secrets set anthropic_api_key", encrypted...)
	if set.code != cli.ExitOK || set.stdout != "anthropic_api_key: set\n" || strings.Contains(set.stderr, stored) {
		t.Fatalf("set on an encrypted database: exit %d stdout %q stderr %q", set.code, set.stdout, set.stderr)
	}
	status := runWithInput(t, "", "secrets status", encrypted...)
	if !strings.Contains(status.stdout, "anthropic_api_key: set\n") || strings.Contains(status.stdout+status.stderr, stored) {
		t.Fatalf("status: %q %q", status.stdout, status.stderr)
	}
	listed := runWithInput(t, "", "settings list", encrypted...)
	if strings.Contains(listed.stdout+listed.stderr, stored) {
		t.Fatalf("settings list printed the key: %q", listed.stdout)
	}

	ran := runWithInput(t, "", "agent run --text hi --kind chat", encrypted...)
	if ran.code != cli.ExitOK || strings.Contains(ran.stdout+ran.stderr, stored) {
		t.Fatalf("agent run with the stored key: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	if got := srv.Requests(); len(got) != 1 || got[0].Header.Get("X-Api-Key") != stored {
		t.Fatalf("requests %d, want one carrying the stored key", len(got))
	}

	pin := append(encrypted, "ANTHROPIC_API_KEY="+pinned)
	pinnedStatus := runWithInput(t, "", "secrets status", pin...)
	if !strings.Contains(pinnedStatus.stdout, "anthropic_api_key: pinned\n") || strings.Contains(pinnedStatus.stdout+pinnedStatus.stderr, pinned) {
		t.Fatalf("pinned status: %q %q", pinnedStatus.stdout, pinnedStatus.stderr)
	}
	overwrite := runWithInput(t, "sk-ant-other-0123456789\n", "secrets set anthropic_api_key", pin...)
	if overwrite.code != cli.ExitError || !strings.Contains(overwrite.stderr, "pinned by env ANTHROPIC_API_KEY") || strings.Contains(overwrite.stderr, pinned) {
		t.Fatalf("set under a pin: exit %d stderr %q", overwrite.code, overwrite.stderr)
	}
	ran = runWithInput(t, "", "agent run --text hi --kind chat", pin...)
	if ran.code != cli.ExitOK || strings.Contains(ran.stdout+ran.stderr, pinned) {
		t.Fatalf("agent run under the pin: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	if got := srv.Requests(); len(got) != 2 || got[1].Header.Get("X-Api-Key") != pinned {
		t.Fatalf("requests %d, want the second to carry the pinned key", len(got))
	}
}

func TestStartupWarnsForACatalogModelTheModelsAPIOmits(t *testing.T) {
	srv := anthropictest.New(t)
	srv.Models = []string{"claude-haiku-5-5", "claude-sonnet-5-5"}
	dir, _ := moduleFixture(t, flagsExcept("assistant", "true"))
	t.Setenv(anthropic.BaseURLEnv, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", anthropictest.APIKey)
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
	dir, _ := moduleFixture(t, flagsExcept("assistant", "true"))
	t.Setenv(anthropic.BaseURLEnv, "http://127.0.0.1:9")
	_, logs := startServeLogged(t, dir)
	waitForLog(t, logs, "model catalog is unchecked")
}
