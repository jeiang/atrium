package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

type pinRow struct {
	Value     string `json:"value"`
	Source    string `json:"source"`
	PinSource string `json:"pin_source"`
}

func pinRowOf(t *testing.T, r settingsRun) pinRow {
	t.Helper()
	if r.code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var row pinRow
	if err := json.Unmarshal([]byte(r.stdout), &row); err != nil {
		t.Fatalf("stdout %q: %v", r.stdout, err)
	}
	return row
}

func TestConfigFilePinsBeatTheDatabaseAndRefuseWrites(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", db)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	t.Setenv("ATRIUM_CONFIG", "")
	const key = "monthly_budget_usd"

	if r := runSettings(t, "set", key, "90"); r.code != cli.ExitOK {
		t.Fatalf("set without config: %d %q", r.code, r.stderr)
	}

	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("atrium.toml", "[assistant]\nmonthly_budget_usd = 120\nmax_steps_chat = \"${PIN_TEST_STEPS}\"\n")
	t.Setenv("ATRIUM_CONFIG", config)
	t.Setenv("PIN_TEST_STEPS", "30")

	got := pinRowOf(t, runSettings(t, "get", key))
	if got.Value != "120" || got.Source != "pin" || got.PinSource != "config "+config+" assistant."+key {
		t.Errorf("config pin: %+v", got)
	}
	if got := pinRowOf(t, runSettings(t, "get", "max_steps_chat")); got.Value != "30" || got.Source != "pin" {
		t.Errorf("interpolated pin: %+v", got)
	}

	for _, verb := range [][]string{{"set", key, "50"}, {"unset", key}} {
		r := runSettings(t, verb...)
		if r.code != cli.ExitError || !strings.Contains(r.stderr, config) || !strings.Contains(r.stderr, "assistant."+key) {
			t.Errorf("%s: exit %d stderr %q", verb[0], r.code, r.stderr)
		}
	}
	if value, ok := storedSetting(t, db, key); !ok || value != "90" {
		t.Errorf("stored value = %q, %v; want 90 untouched", value, ok)
	}

	t.Setenv("ATRIUM_ASSISTANT_MONTHLY_BUDGET_USD", "200")
	got = pinRowOf(t, runSettings(t, "get", key))
	if got.Value != "200" || got.PinSource != "env ATRIUM_ASSISTANT_MONTHLY_BUDGET_USD" {
		t.Errorf("env pin over config: %+v", got)
	}
	t.Setenv("ATRIUM_ASSISTANT_MONTHLY_BUDGET_USD", "")

	secret := write("budget", " 130\n")
	t.Setenv("ATRIUM_CONFIG", write("file.toml", "[assistant]\nmonthly_budget_usd_file = "+`"`+secret+`"`+"\n"))
	if got := pinRowOf(t, runSettings(t, "get", key)); got.Value != "130" || got.Source != "pin" {
		t.Errorf("file pin: %+v", got)
	}

	for name, c := range map[string]struct{ body, names string }{
		"unknown key":     {"[assistant]\nnope = 1\n", "assistant.nope"},
		"unknown module":  {"[nowhere]\nx = 1\n", "nowhere"},
		"undefined env":   {"[assistant]\nmax_steps_chat = \"${PIN_TEST_UNDEFINED}\"\n", "assistant.max_steps_chat"},
		"missing file":    {"[assistant]\nmonthly_budget_usd_file = \"" + filepath.Join(dir, "absent") + "\"\n", "assistant.monthly_budget_usd_file"},
		"invalid value":   {"[assistant]\nmonthly_budget_usd = -1\n", "assistant.monthly_budget_usd"},
		"both file+value": {"[assistant]\nmonthly_budget_usd = 1\nmonthly_budget_usd_file = \"" + secret + "\"\n", "assistant.monthly_budget_usd"},
	} {
		t.Setenv("ATRIUM_CONFIG", write("bad.toml", c.body))
		r := runSettings(t, "get", key)
		if r.code != cli.ExitError || !strings.Contains(r.stderr, c.names) || r.stdout != "" {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, r.code, r.stdout, r.stderr)
		}
	}
	t.Setenv("ATRIUM_CONFIG", write("bad.toml", "[assistant]\nmax_steps_chat = \"${PIN_TEST_UNDEFINED}\"\n"))
	if r := runSettings(t, "get", key); !strings.Contains(r.stderr, "PIN_TEST_UNDEFINED") {
		t.Errorf("stderr %q does not name the variable", r.stderr)
	}
}
