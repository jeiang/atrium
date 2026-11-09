package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	Port         = 587
	Timeout      = 60 * time.Second
	ICloudDomain = "icloud.com"
)

var log = logx.Logger("atrium.smtp")

type Credentials func(ctx context.Context, account string) (user, password string, ok bool)

func SenderAddress(preset, user string) string {
	if preset == config.ICloud && !strings.Contains(user, "@") {
		return user + "@" + ICloudDomain
	}
	return user
}

func Send(ctx context.Context, creds Credentials, account string, msg *Message, recipients []string) (Refusals, error) {
	user, password, ok := creds(ctx, account)
	if !ok {
		return nil, &Failure{Kind: "Config", Msg: account + " is not configured: no enabled account with a login and password"}
	}
	if len(recipients) == 0 {
		return nil, &Failure{Kind: "NoRecipients", Msg: "no recipients"}
	}
	refused, err := send(ctx, defaultFactory(), account, user, password, msg, recipients, nil, nil)
	var unknown *OutcomeUnknown
	if errors.As(err, &unknown) {
		return nil, unknown
	}
	if err != nil {
		return nil, err
	}
	return refused, nil
}

func defaultFactory() Factory {
	d := Dialer{}
	if path, ok := config.OS("ATRIUM_SMTP_CA"); ok && path != "" {
		if pem, err := os.ReadFile(path); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				d.TLSConfig = &tls.Config{RootCAs: pool}
			}
		}
	}
	return d.Dial
}

func send(ctx context.Context, factory Factory, account, user, password string, msg *Message, recipients []string,
	token func(context.Context) (string, error), invalidate func()) (refused Refusals, err error) {
	spec, _ := config.From(ctx).Account(account)
	host, port := spec.SMTPHost(), Port
	if addr, ok := config.OS("ATRIUM_SMTP_ADDR"); ok && addr != "" {
		h, p, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			return nil, splitErr
		}
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return nil, convErr
		}
		host, port = h, n
	}
	server, err := factory(ctx, host, port, Timeout)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		qerr := server.Quit()
		switch {
		case qerr == nil:
		case accepted:
			log.Warn(fmt.Sprintf("%s SMTP session not closed cleanly after the mail was accepted: %s", account, qerr))
		default:
			if err != nil {
				log.Warn(fmt.Sprintf("%s SMTP session not closed cleanly after a failed send: %s", account, qerr))
				break
			}
			refused, err = nil, qerr
		}
	}()
	if err := server.StartTLS(); err != nil {
		return nil, err
	}
	if token != nil {
		auth, ok := server.(XOAUTH2Session)
		if !ok {
			return nil, &Failure{Kind: "Config", Msg: "this SMTP transport cannot use XOAUTH2"}
		}
		if err := auth.LoginXOAUTH2(ctx, user, token, invalidate); err != nil {
			return nil, err
		}
	} else if err := server.Login(user, password); err != nil {
		return nil, err
	}
	refused, err = server.SendMessage(msg, SenderAddress(spec.Preset, user), recipients)
	accepted = err == nil
	return refused, err
}
