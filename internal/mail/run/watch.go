package run

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/runtime"
)

var watchLog = logx.Logger("atrium.watch")

const (
	idleWait   = config.IdleSeconds * time.Second
	noIdleWait = config.IdleSeconds * time.Second / 30
	watchBeat  = "watch"
)

var watchBackoff = runtime.Backoff{
	Initial:    mail.TuningMailBackoffStartSeconds * time.Second,
	Maximum:    mail.TuningMailBackoffMaxSeconds * time.Second,
	ResetAfter: noIdleWait,
}

func ConnectionLost(err error) bool {
	var interrupted *mail.WriteInterrupted
	if errors.As(err, &interrupted) || errors.Is(err, imapclient.ErrReadonly) {
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
	Env         config.Env
	Cfg         config.Config
	DB          *db.DB
	Open        mail.OpenFunc
	Classifiers Classifier
}

func WatchServiceName(account string) string { return "mail-watch/" + account }

func AccountRevision(ctx context.Context, account string) string {
	spec, _ := config.From(ctx).Account(account)
	user, password, _ := config.Credential(ctx, account)
	sum := sha256.Sum256(fmt.Appendf(nil, "%+v\x00%s\x00%s", spec, user, password))
	return hex.EncodeToString(sum[:])
}

func (w *Watcher) AccountService(account string) runtime.ServiceDefinition {
	return runtime.ServiceDefinition{
		Name:    WatchServiceName(account),
		Backoff: watchBackoff,
		Stall:   2 * idleWait,
		Run: func(ctx context.Context, s *runtime.Services) error {
			err := w.run(ctx, s, account)
			if err == nil || ctx.Err() != nil {
				return err
			}
			if ConnectionLost(err) {
				watchLog.Warn(fmt.Sprintf("%s connection lost (%s), reconnecting", account, err))
			} else {
				watchLog.Error(account+" watch failed, retrying", logx.Err, err.Error())
			}
			return err
		},
	}
}

func waiting(beat func(), fn func()) {
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
				beat()
			}
		}
	}()
	fn()
	close(stop)
	<-done
	beat()
}

func settleFor(ctx context.Context, q db.Querier) (time.Duration, error) {
	return time.Duration(mail.TuningMailIdleSettleSeconds) * time.Second, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func waitForChange(ctx context.Context, client mail.Client) (bool, error) {
	if !client.HasCapability("IDLE") {
		sleepCtx(ctx, noIdleWait)
		return true, client.Noop(ctx)
	}
	if err := client.Select(ctx, config.Inbox); err != nil {
		return false, err
	}
	return client.IdleChanges(ctx, idleWait)
}

func (w *Watcher) run(ctx context.Context, s *runtime.Services, account string) error {
	beat := func() { s.Heartbeat(watchBeat) }
	beat()
	pass, err := prepare(ctx, w.Env, w.DB, w.Cfg, w.Open, nil, w.Classifiers)
	if err != nil {
		return err
	}
	locked := func() error {
		work, release, err := s.Work(ctx)
		if err != nil {
			return err
		}
		defer release()
		return pass.Locked(ctx, work, []string{account})
	}
	if err := locked(); err != nil {
		return err
	}
	return mailops.Session(ctx, pass.Open, pass.DB.Read, account, func(client mail.Client) error {
		for ctx.Err() == nil {
			beat()
			var changed bool
			var err error
			waiting(beat, func() { changed, err = waitForChange(ctx, client) })
			if ctx.Err() != nil {
				break
			}
			if err != nil {
				return err
			}
			if changed {
				settle, err := settleFor(ctx, pass.DB.Read)
				if err != nil {
					return err
				}
				waiting(beat, func() { sleepCtx(ctx, settle) })
				if ctx.Err() != nil {
					break
				}
			}
			if err := locked(); err != nil {
				return err
			}
		}
		return nil
	})
}
