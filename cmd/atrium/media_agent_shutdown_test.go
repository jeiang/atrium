package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
)

func slowThumbnailAgent(t *testing.T, dir string) func() []int {
	t.Helper()
	pids := filepath.Join(t.TempDir(), "pids")
	bin := fakeFFmpeg(t, "echo $$ >> "+pids+"\nsleep 600 &\necho $! >> "+pids+"\nwait\n")
	libraryAgent(t, dir, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, media.JobThumbnails)
	return func() []int {
		raw, _ := os.ReadFile(pids)
		var ids []int
		for _, f := range strings.Fields(string(raw)) {
			if n, err := strconv.Atoi(f); err == nil {
				ids = append(ids, n)
			}
		}
		return ids
	}
}

func waitFFmpegGone(t *testing.T, ids []int) {
	t.Helper()
	for _, pid := range ids {
		waitUntil(t, 15*time.Second, "ffmpeg process "+strconv.Itoa(pid)+" to be gone", func() bool {
			return syscall.Kill(pid, 0) == syscall.ESRCH
		})
	}
}

func TestShutdownWhileThumbnailsWaitOnASlowAgent(t *testing.T) {
	t.Run("serve", func(t *testing.T) {
		dir, _, f := moduleFixture(t, flagsExcept("", ""))
		running := slowThumbnailAgent(t, dir)
		addrFile := filepath.Join(f.dir, "serve-addr")
		cmd := cliExitProcessArgs(t, []string{"serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local"})
		cmd.Env = append(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_PUBLIC_HOST=", "ATRIUM_SERVE_ADDR_FILE="+addrFile)
		serve := startServeProcess(t, cmd, addrFile)
		waitUntil(t, 30*time.Second, "the thumbnail job to reach the agent's ffmpeg", func() bool { return len(running()) == 2 })
		start := time.Now()
		if code := serve.stop(t, syscall.SIGTERM); code != cli.ExitOK {
			t.Fatalf("serve exit %d: %s", code, serve.stderr.String())
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Fatalf("serve took %s to stop while the thumbnails job waited", took)
		}
		waitFFmpegGone(t, running())
	})
	t.Run("tick", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, flagsExcept("", ""))
		running := slowThumbnailAgent(t, dir)
		tick := cliExitProcessArgs(t, []string{"agent", "tick"})
		if err := tick.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tick.Process.Kill() })
		waitUntil(t, 30*time.Second, "the thumbnail job to reach the agent's ffmpeg", func() bool { return len(running()) == 2 })
		if err := tick.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		if code := exitWithin(t, tick, 20*time.Second); code != cli.ExitOK && code != cli.ExitInterrupt {
			t.Fatalf("agent tick exit %d after SIGINT", code)
		}
		waitFFmpegGone(t, running())
	})
}
