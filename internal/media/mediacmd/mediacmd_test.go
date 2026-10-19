package mediacmd_test

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/media/mediacmd"
)

func run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	app := cli.New("atrium", nil)
	app.Add(mediacmd.Command(nil))
	var out, errOut bytes.Buffer
	code := app.Run(t.Context(), args, &out, &errOut)
	if code != cli.ExitOK {
		t.Logf("stderr: %s", errOut.String())
	}
	return code, out.String()
}

func TestUnconfiguredMediaJobsReportSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("QBITTORRENT_URL", "")

	if code, out := run(t, "media", "poll"); code != cli.ExitOK || out != "{}\n" {
		t.Errorf("poll without channels = %d %q", code, out)
	}

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), d, path); err != nil {
		t.Fatal(err)
	}
	if _, err := media.AddChannel(context.Background(), d.Write, "L", "tt1234567", "x", false, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	want := "{\n  \"skipped\": \"QBITTORRENT_URL is not set\"\n}\n"
	for _, sub := range []string{"poll", "sweep"} {
		if code, out := run(t, "media", sub); code != cli.ExitOK || out != want {
			t.Errorf("media %s = %d %q, want %q", sub, code, out, want)
		}
	}
}

func TestCallablesAreTheBuiltinMediaJobs(t *testing.T) {
	got := mediacmd.Callables()
	for _, name := range []string{scheduler.MediaChannelsName, scheduler.MediaSweepName} {
		if got[name] == nil || !slices.Contains(scheduler.CallableNames, name) {
			t.Errorf("%s is not a registered callable", name)
		}
	}
	if len(got) != 2 {
		t.Errorf("callables = %d, want the channel poll and the sweep", len(got))
	}
}
