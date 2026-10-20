package run

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/sdnotify"
)

var watchLog = logx.Logger("atrium.watch")

const (
	idleWait       = config.IdleSeconds * time.Second
	noIdleWait     = config.IdleSeconds * time.Second / 30
	settle         = config.IdleSettleSeconds * time.Second
	shutdownWindow = 30 * time.Second
)

func NextBackoff(current int) int {
	if current == 0 {
		return config.BackoffStart
	}
	return min(current*2, config.BackoffMax)
}

func ConnectionLost(err error) bool {
	var classed py.Classed
	if errors.As(err, &classed) && (classed.PyClass() == "abort" || classed.PyClass() == "readonly") {
		return true
	}
	var (
		netErr   net.Error
		pathErr  *fs.PathError
		linkErr  *os.LinkError
		sysErr   *os.SyscallError
		errno    syscall.Errno
		record   tls.RecordHeaderError
		verify   *tls.CertificateVerificationError
		authErr  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		invalid  x509.CertificateInvalidError
		alertErr tls.AlertError
	)
	return errors.As(err, &netErr) || errors.As(err, &pathErr) || errors.As(err, &linkErr) || errors.As(err, &sysErr) ||
		errors.As(err, &errno) || errors.As(err, &record) || errors.As(err, &verify) || errors.As(err, &authErr) ||
		errors.As(err, &hostErr) || errors.As(err, &invalid) || errors.As(err, &alertErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

type Watcher struct {
	Cfg   config.Config
	Open  mailops.Opener
	Pass  func(d *db.DB) *Pass
	Sleep func(ctx context.Context, d time.Duration)

	Notify *sdnotify.Notifier

	mu       sync.Mutex
	migrated bool
	beats    map[string]*sdnotify.Heartbeat
}

func (w *Watcher) heartbeat(account string) *sdnotify.Heartbeat {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.beats == nil {
		w.beats = map[string]*sdnotify.Heartbeat{}
	}
	h := w.beats[account]
	if h == nil {
		h = sdnotify.NewHeartbeat()
		w.beats[account] = h
	}
	return h
}

func (w *Watcher) alive(maxAge time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, h := range w.beats {
		if !h.Fresh(maxAge) {
			return false
		}
	}
	return true
}

func waiting(h *sdnotify.Heartbeat, fn func()) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				h.Beat()
			}
		}
	}()
	fn()
	close(stop)
	<-done
	h.Beat()
}

func (w *Watcher) connect(ctx context.Context) (*Pass, func(), error) {
	d, err := db.Connect(w.Cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.migrated {
		if err := db.Migrate(ctx, d, w.Cfg.DBPath); err != nil {
			_ = d.Close()
			return nil, nil, err
		}
		w.migrated = true
		w.Notify.Ready()
		w.Notify.Status("watching")
	}
	if w.Pass != nil {
		return w.Pass(d), func() { _ = d.Close() }, nil
	}
	return New(d, w.Cfg, w.Open, nil), func() { _ = d.Close() }, nil
}

func (w *Watcher) sleep(ctx context.Context, d time.Duration) {
	if w.Sleep != nil {
		w.Sleep(ctx, d)
		return
	}
	sleepCtx(ctx, d)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (w *Watcher) waitForChange(ctx context.Context, client mail.Client) (bool, error) {
	if !client.HasCapability("IDLE") {
		w.sleep(ctx, noIdleWait)
		return true, client.Noop(ctx)
	}
	if err := client.Select(ctx, config.Inbox); err != nil {
		return false, err
	}
	return client.IdleChanges(ctx, idleWait)
}

func (w *Watcher) Account(ctx context.Context, account string) {
	backoff := 0
	h := w.heartbeat(account)
	for ctx.Err() == nil {
		h.Beat()
		err := w.cycle(ctx, account, &backoff)
		if err == nil || ctx.Err() != nil {
			continue
		}
		backoff = NextBackoff(backoff)
		reason := "watch failed"
		if ConnectionLost(err) {
			reason = "connection lost"
			watchLog.Warn(fmt.Sprintf("%s connection lost (%s), reconnecting in %ds", account, err, backoff))
		} else {
			watchLog.Error(fmt.Sprintf("%s watch failed, retrying in %ds", account, backoff), logx.Err, err.Error())
		}
		w.Notify.Status(fmt.Sprintf("%s: reconnecting in %ds (%s)", account, backoff, reason))
		waiting(h, func() { w.sleep(ctx, time.Duration(backoff)*time.Second) })
	}
}

func (w *Watcher) cycle(ctx context.Context, account string, backoff *int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	pass, closeDB, err := w.connect(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	workCtx := context.WithoutCancel(ctx)
	if err := pass.Locked(ctx, workCtx, []string{account}); err != nil {
		return err
	}
	w.Notify.Status("watching")
	h := w.heartbeat(account)
	return mailops.Session(ctx, pass.Open, account, func(client mail.Client) error {
		*backoff = 0
		for ctx.Err() == nil {
			h.Beat()
			var changed bool
			var err error
			waiting(h, func() { changed, err = w.waitForChange(ctx, client) })
			if ctx.Err() != nil {
				break
			}
			if err != nil {
				return err
			}
			if changed {
				waiting(h, func() { w.sleep(ctx, settle) })
				if ctx.Err() != nil {
					break
				}
			}
			if err := pass.Locked(ctx, workCtx, []string{account}); err != nil {
				return err
			}
		}
		return nil
	})
}

func Watch(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) error {
	accounts, err := SelectAccounts(cfg, requested)
	if err != nil {
		return err
	}
	notifier := sdnotify.FromEnv()
	w := &Watcher{Cfg: cfg, Open: open, Notify: notifier}
	for _, account := range accounts {
		w.heartbeat(account)
	}
	go notifier.RunWatchdog(ctx, func() bool { return w.alive(notifier.Interval() * 2) })
	var wg sync.WaitGroup
	for _, account := range accounts {
		wg.Go(func() { w.Account(ctx, account) })
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		notifier.Stopping()
		select {
		case <-done:
		case <-time.After(shutdownWindow):
		}
	}
	return nil
}
