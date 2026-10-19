package run

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type cliGolden struct {
	Name     string            `json:"name"`
	Argv     []string          `json:"argv"`
	ExitCode int               `json:"exit_code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
	Env      map[string]string `json:"env"`
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCLIGoldens runs every recorded `sync` and `watch` invocation on a copy of the golden database, with the
// recorded environment changes and fake clients for the accounts (a `watch` ends after its first wait, as the
// generator drives it), and compares the exit code, stdout and, for a failure, the last line of stderr.
func TestCLIGoldens(t *testing.T) {
	var index []cliGolden
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, g := range index {
		if len(g.Argv) < 2 || g.Argv[1] != "sync" && g.Argv[1] != "watch" {
			continue
		}
		ran++
		t.Run(g.Name, func(t *testing.T) {
			w := newWorld(t, config.Shadow, nil)
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "atrium.db")
			copyFile(t, filepath.Join(golden.Dir(), "atrium.db"), dbPath)
			t.Setenv("ATRIUM_DB", dbPath)
			t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
			t.Setenv("ATRIUM_MODE", config.Shadow)
			for k, v := range credentials {
				t.Setenv(k, v)
			}
			for k, v := range g.Env {
				t.Setenv(k, v)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			w.Wrap = func(_ string, c mail.Client) mail.Client {
				return idler{c, func(context.Context) (bool, error) {
					cancel()
					return false, nil
				}}
			}
			var stdout, stderr bytes.Buffer
			code := app(w).Run(ctx, g.Argv[1:], &stdout, &stderr)
			if code != g.ExitCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, g.ExitCode, stderr.String())
			}
			if want := string(golden.ReadFile(t, "cli", g.Stdout)); stdout.String() != want {
				t.Errorf("stdout = %q, want %q", stdout.String(), want)
			}
			if g.ExitCode != 0 {
				if got, want := lastLine(stderr.String()), lastLine(string(golden.ReadFile(t, "cli", g.Stderr))); got != want {
					t.Errorf("last stderr line = %q, want %q", got, want)
				}
			}
			if ctx.Err() == context.DeadlineExceeded {
				t.Error("the command did not finish")
			}
		})
	}
	if ran == 0 {
		t.Fatal("no sync or watch goldens")
	}
}
