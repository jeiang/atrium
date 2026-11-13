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
	thumbMaxBytes = 8 << 20
	thumbFormats  = "matroska,mov,avi,mpegts"
)

var (
	errNoImage       = errors.New("the video has no thumbnail")
	errNoThumbnailer = errors.New("ffmpeg and ffprobe are not available")
	errThumbFailed   = errors.New("ffmpeg failed on the agent side")
	thumbInputArgs   = []string{"-nostdin", "-v", "error", "-protocol_whitelist", "fd", "-format_whitelist", thumbFormats, "-threads", "1"}
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

type cappedBuffer struct {
	b    []byte
	max  int
	over bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := min(len(p), c.max-len(c.b))
	c.b = append(c.b, p[:n]...)
	c.over = c.over || n < len(p)
	return len(p), nil
}

func (t *thumbnailer) run(parent context.Context, input *os.File, stdout io.Writer, argv ...string) (int, error) {
	ctx, cancel := context.WithTimeout(parent, ffmpegTimeout)
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
	if err := parent.Err(); err != nil {
		return -1, err
	}
	if ctx.Err() != nil {
		return -1, fmt.Errorf("%w: %w", errThumbFailed, ctx.Err())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return -1, fmt.Errorf("%w: %s", errThumbFailed, ws.Signal())
		}
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
	none := func() error {
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
	out := &cappedBuffer{max: 4096}
	code, err := a.thumbs.run(ctx, t.file, out, append(append([]string{a.thumbs.ffprobe}, thumbInputArgs...), "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", "-fd", "3", "-i", "fd:")...)
	if err != nil {
		return err
	}
	at := 0.0
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out.b)), 64); code == 0 && err == nil && seconds*thumbPosition > 0 {
		at = seconds * thumbPosition
	}
	image := &cappedBuffer{max: thumbMaxBytes}
	code, err = a.thumbs.run(ctx, t.file, image, append(append([]string{a.thumbs.ffmpeg}, thumbInputArgs...), "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-fd", "3", "-i", "fd:", "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-2", thumbWidth), "-q:v", "4", "-f", "mjpeg", "pipe:1")...)
	if err != nil {
		return err
	}
	if code != 0 || len(image.b) == 0 || image.over {
		return none()
	}
	tmpFile, err := os.CreateTemp(dir, ".thumb-*.jpg")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer func() { _ = os.Remove(tmp) }()
	_, err = tmpFile.Write(image.b)
	if err == nil {
		err = tmpFile.Sync()
	}
	if cerr := tmpFile.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, t.final); err != nil {
		return err
	}
	return syncDir(dir)
}

func pruneThumbnails(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if strings.HasPrefix(name, ".thumb-") {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		if info, err := e.Info(); err == nil && strings.HasSuffix(name, ".none") && time.Since(info.ModTime()) >= thumbMissTTL {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
