package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
)

type ffmpegTree struct {
	bin, pids string
}

func newFFmpegTree(t *testing.T, dir string) ffmpegTree {
	t.Helper()
	tree := ffmpegTree{bin: filepath.Join(dir, "bin"), pids: filepath.Join(dir, "pids")}
	for _, path := range []string{tree.bin, tree.pids} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\necho $$ > " + strconv.Quote(filepath.Join(tree.pids, "leader")) + "\nsleep 600 &\necho $! > " +
		strconv.Quote(filepath.Join(tree.pids, "child")) + "\nwait\n"
	for _, name := range []string{"ffprobe", "ffmpeg"} {
		if err := os.WriteFile(filepath.Join(tree.bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	episode := filepath.Join(dir, "media", media.TV, "Show", "Season 1")
	if err := os.MkdirAll(episode, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(episode, "Show.S01E01.mkv"), []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tree
}

func (tree ffmpegTree) env(cmd *exec.Cmd) {
	cmd.Env = append(cmd.Env, "PATH="+tree.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (tree ffmpegTree) started() bool {
	for _, name := range []string{"leader", "child"} {
		raw, err := os.ReadFile(filepath.Join(tree.pids, name))
		if err != nil || strings.TrimSpace(string(raw)) == "" {
			return false
		}
	}
	return true
}

func (tree ffmpegTree) pid(t *testing.T, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(tree.pids, name))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func (tree ffmpegTree) waitGone(t *testing.T) {
	t.Helper()
	for _, name := range []string{"leader", "child"} {
		pid := tree.pid(t, name)
		waitUntil(t, 10*time.Second, "ffmpeg "+name+" process "+strconv.Itoa(pid)+" to be gone", func() bool {
			return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
		})
	}
}

func TestThumbnailCancelKillsTheFFmpegTree(t *testing.T) {
	t.Run("serve", func(t *testing.T) {
		dir, _, f := moduleFixture(t, flagsExcept("", ""))
		tree := newFFmpegTree(t, dir)
		d := asstDB(t, dir)
		declareJobsAndParkThem(t, d)
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, media.JobThumbnails)

		addrFile := filepath.Join(f.dir, "serve-addr")
		cmd := cliExitProcessArgs(t, []string{"serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local"})
		cmd.Env = append(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_DB="+filepath.Join(f.dir, "atrium.db"), "ATRIUM_CACHE_DIR="+filepath.Join(f.dir, "cache"),
			"ATRIUM_PUBLIC_HOST=", "ATRIUM_SERVE_ADDR_FILE="+addrFile)
		tree.env(cmd)
		serve := startServeProcess(t, cmd, addrFile)
		waitUntil(t, 30*time.Second, "the thumbnail job to start ffmpeg", tree.started)
		if code := serve.stop(t, syscall.SIGTERM); code != cli.ExitOK {
			t.Fatalf("serve exit %d: %s", code, serve.stderr.String())
		}
		tree.waitGone(t)
	})
	t.Run("tick", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, flagsExcept("", ""))
		tree := newFFmpegTree(t, dir)
		d := asstDB(t, dir)
		declareJobsAndParkThem(t, d)
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, media.JobThumbnails)

		tick := cliExitProcessArgs(t, []string{"agent", "tick"})
		tree.env(tick)
		if err := tick.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tick.Process.Kill() })
		waitUntil(t, 30*time.Second, "the thumbnail job to start ffmpeg", tree.started)
		if err := tick.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		if code := exitWithin(t, tick, 30*time.Second); code != cli.ExitOK && code != cli.ExitInterrupt {
			t.Fatalf("agent tick exit %d after SIGINT", code)
		}
		tree.waitGone(t)
	})
}
