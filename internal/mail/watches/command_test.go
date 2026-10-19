package watches_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/watches"
)

func TestWatchesRefreshCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	app := cli.New("atrium", nil)
	app.Add(watches.Command())
	var out, errOut bytes.Buffer
	if code := app.Run(t.Context(), []string{"watches", "refresh"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	want := "{\n  \"packages\": {\n    \"scanned\": 0,\n    \"found\": 0\n  }\n}\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}
