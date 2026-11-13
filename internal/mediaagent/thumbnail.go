//go:build linux || darwin

package mediaagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

const (
	thumbWidth    = 480
	thumbPosition = 0.1
	ffmpegTimeout = 120 * time.Second
	ffmpegNice    = "10"
	thumbMissTTL  = 7 * 24 * time.Hour
	thumbVersion  = "v1"
	thumbInput    = "/dev/fd/3"
)

var (
	errNoImage       = errors.New("the video has no thumbnail")
	errNoThumbnailer = errors.New("ffmpeg and ffprobe are not available")
)

type thumbnailer struct {
	ffmpeg  string
	ffprobe string
	nice    string
	mu      sync.Mutex
}

func newThumbnailer() *thumbnailer {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil || !filepath.IsAbs(ffmpeg) {
		return nil
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil || !filepath.IsAbs(ffprobe) {
		return nil
	}
	nice, err := exec.LookPath("nice")
	if err != nil || !filepath.IsAbs(nice) {
		nice = ""
	}
	return &thumbnailer{ffmpeg: ffmpeg, ffprobe: ffprobe, nice: nice}
}

type cappedBuffer struct{ b []byte }

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.b = append(c.b, p[:min(len(p), 4096-len(c.b))]...)
	return len(p), nil
}

func (t *thumbnailer) run(ctx context.Context, input *os.File, stdout io.Writer, argv ...string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()
	if t.nice != "" {
		argv = append([]string{t.nice, "-n", ffmpegNice}, argv...)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return -1, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.ExtraFiles = []*os.File{input}
	cmd.Stdout = stdout
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if cmd.Process != nil {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

type thumbTarget struct {
	file  *os.File
	final string
}

func (a *Agent) thumbTarget(p protocol.Path) (thumbTarget, error) {
	if !library(p.Root) {
		return thumbTarget{}, ErrUnsafePath
	}
	f, st, err := a.roots.openFile(p.Root, p.Rel)
	if err != nil {
		return thumbTarget{}, err
	}
	id := identity(st)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%s", p.Root, p.Rel, id.Device, id.Inode, id.Size, id.MTimeNS, thumbVersion))
	return thumbTarget{file: f, final: filepath.Join(a.cfg.CacheDirectory, "thumbs", hex.EncodeToString(sum[:])+".jpg")}, nil
}

func thumbCached(final string) (hit, miss bool) {
	if info, err := os.Lstat(final); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return true, false
	}
	info, err := os.Lstat(final + ".none")
	return false, err == nil && time.Since(info.ModTime()) < thumbMissTTL
}

func (a *Agent) generateThumbnail(ctx context.Context, t thumbTarget) error {
	if a.thumbs == nil {
		return errNoThumbnailer
	}
	a.thumbs.mu.Lock()
	defer a.thumbs.mu.Unlock()
	if hit, miss := thumbCached(t.final); hit {
		return nil
	} else if miss {
		return errNoImage
	}
	none := func(err error) error {
		if ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errNoImage) {
			return err
		}
		marker := t.final + ".none"
		_ = os.Remove(marker)
		if f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
			_ = f.Close()
		}
		return errNoImage
	}
	dir := filepath.Dir(t.final)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out := &cappedBuffer{}
	code, err := a.thumbs.run(ctx, t.file, out, a.thumbs.ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", thumbInput)
	if err != nil {
		return none(err)
	}
	at := 0.0
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out.b)), 64); code == 0 && err == nil && seconds*thumbPosition > 0 {
		at = seconds * thumbPosition
	}
	tmpFile, err := os.CreateTemp(dir, ".thumb-*.jpg")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := tmpFile.Close(); err != nil {
		return err
	}
	code, err = a.thumbs.run(ctx, t.file, nil, a.thumbs.ffmpeg, "-v", "error", "-y", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", thumbInput, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-2", thumbWidth), "-q:v", "4", tmp)
	if err != nil {
		return none(err)
	}
	if info, err := os.Lstat(tmp); code != 0 || err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return none(errNoImage)
	}
	if err := os.Rename(tmp, t.final); err != nil {
		return err
	}
	return syncDir(dir)
}
