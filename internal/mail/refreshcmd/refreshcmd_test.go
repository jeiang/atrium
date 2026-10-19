package refreshcmd_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/refreshcmd"
	"github.com/jeiang/atrium/internal/py"
)

func TestExecuteRunsUnderTheExclusiveLockAndPrintsIndentedJSON(t *testing.T) {
	dir := t.TempDir()
	env := config.Map(map[string]string{"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache")})
	var out, errOut bytes.Buffer
	locked := false
	run := func(_ context.Context, d *db.DB, cfg config.Config) py.Dict {
		f, err := os.OpenFile(cfg.LockPath(), os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return nil
		}
		defer func() { _ = f.Close() }()
		locked = errors.Is(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB), syscall.EWOULDBLOCK)
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM open_loops").Scan(&n); err != nil {
			t.Error("the database must be migrated:", err)
		}
		return py.Dict{{Key: "loops", Value: py.Dict{{Key: "owe_reply", Value: n}}}, {Key: "reminders", Value: nil}}
	}
	code := refreshcmd.Execute(&cli.Env{Ctx: t.Context(), Stdout: &out, Stderr: &errOut}, env, run)
	if code != cli.ExitOK || errOut.Len() != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if !locked {
		t.Error("the pass ran without the database's exclusive lock")
	}
	if want := "{\n  \"loops\": {\n    \"owe_reply\": 0\n  },\n  \"reminders\": null\n}\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

func TestExecuteReportsConfigurationErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	code := refreshcmd.Execute(&cli.Env{Ctx: t.Context(), Stdout: &out, Stderr: &errOut}, config.Map(nil),
		func(context.Context, *db.DB, config.Config) py.Dict { t.Error("ran"); return nil })
	if code != cli.ExitError || !strings.Contains(errOut.String(), "ATRIUM_DB or STATE_DIRECTORY is required") || out.Len() != 0 {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

func TestCommandIsTheGroupWithRefresh(t *testing.T) {
	cmd := refreshcmd.Command("loops", nil)
	if cmd.Name != "loops" || len(cmd.Commands) != 1 || cmd.Commands[0].Name != "refresh" || cmd.Commands[0].Setup == nil {
		t.Errorf("command = %+v", cmd)
	}
}
