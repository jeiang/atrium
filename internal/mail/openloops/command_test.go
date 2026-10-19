package openloops_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/openloops"
)

func TestLoopsRefreshCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_TZ", tz)
	app := cli.New("atrium", nil)
	app.Add(openloops.Command(nil))
	var out, errOut bytes.Buffer
	if code := app.Run(t.Context(), []string{"loops", "refresh"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	want := "{\n  \"backfilled\": 0,\n  \"loops\": {\n    \"owe_reply\": 0,\n    \"waiting_on_them\": 0\n  },\n  \"commitments\": 0,\n  \"reminders\": 0\n}\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}
