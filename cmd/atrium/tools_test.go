package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestDisabledToolIsNotOffered(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('tool_profile_set_enabled', 'false')"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	ran := runApp("agent", "run", "--text", "hi")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	names := offeredTools(t, api, ran)
	if slices.Contains(names, "profile_set") {
		t.Errorf("disabled profile_set was offered: %v", names)
	}
	if !slices.Contains(names, "profile_get") {
		t.Errorf("enabled profile_get was not offered: %v", names)
	}
}
