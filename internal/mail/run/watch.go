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
)

var watchLog = logx.Logger("atrium.watch")

const (
	idleWait       = config.IdleSeconds * time.Second
	noIdleWait     = config.IdleSeconds * time.Second / 30
	settle         = config.IdleSettleSeconds * time.Second
	shutdownWindow = 30 * time.Second
)

// NextBackoff is next_backoff: 0, then BackoffStart, doubling up to BackoffMax seconds.
func NextBackoff(current int) int {
	if current == 0 {
		return config.BackoffStart
	}
	return min(current*2, config.BackoffMax)
}

// ConnectionLost is the `except (imaplib.IMAP4.abort, OSError)` of watch_account: an imaplib abort (readonly is
// one) or a socket, TLS or file system failure.
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

// Watcher is watch.py: Cfg and Open are the configuration and the accounts' clients, Pass builds a cycle's pass
// over its connection (New when nil), Sleep is stop.wait, returning early when ctx ends (sleepCtx when nil).
type Watcher struct {
	Cfg   config.Config
	Open  mailops.Opener
	Pass  func(d *db.DB) *Pass
	Sleep func(ctx context.Context, d time.Duration)

	mu       sync.Mutex
	migrated bool
}

// connect is the db.connect at the top of each cycle: the schema is migrated by the first one that succeeds.
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

// waitForChange is wait_for_change: IDLE on the Inbox until a change or IdleSeconds pass; without IDLE, a wait of
// IdleSeconds/30 and a NOOP, which always counts as a change.
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

// Account is watch_account: a locked pass, then a pass after every change the Inbox reports, until ctx ends. A
// failure closes the connection and reconnects after the backoff, which a successful first pass resets. A pass that
// has started runs to its end: ctx is seen only between passes and while waiting.
func (w *Watcher) Account(ctx context.Context, account string) {
	backoff := 0
	for ctx.Err() == nil {
		err := w.cycle(ctx, account, &backoff)
		if err == nil || ctx.Err() != nil {
			continue
		}
		backoff = NextBackoff(backoff)
		if ConnectionLost(err) {
			watchLog.Warn(fmt.Sprintf("%s connection lost (%s), reconnecting in %ds", account, err, backoff))
		} else {
			watchLog.Error(fmt.Sprintf("%s watch failed, retrying in %ds", account, backoff), logx.Err, err.Error())
		}
		w.sleep(ctx, time.Duration(backoff)*time.Second)
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
	whole := context.WithoutCancel(ctx)
	if err := pass.Locked(whole, []string{account}); err != nil {
		return err
	}
	return mailops.Session(ctx, pass.Open, account, func(client mail.Client) error {
		*backoff = 0
		for ctx.Err() == nil {
			changed, err := w.waitForChange(ctx, client)
			if ctx.Err() != nil {
				break
			}
			if err != nil {
				return err
			}
			if changed {
				w.sleep(ctx, settle)
				if ctx.Err() != nil {
					break
				}
			}
			if err := pass.Locked(whole, []string{account}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Watch is watch(): one watch loop per selected account until ctx ends, then up to 30 s for the loops to stop.
func Watch(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) error {
	accounts, err := SelectAccounts(cfg, requested)
	if err != nil {
		return err
	}
	w := &Watcher{Cfg: cfg, Open: open}
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
		select {
		case <-done:
		case <-time.After(shutdownWindow):
		}
	}
	return nil
}
