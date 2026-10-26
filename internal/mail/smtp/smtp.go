package smtp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	Port         = 587
	Timeout      = 60 * time.Second
	ICloudDomain = "icloud.com"
)

var log = logx.Logger("atrium.smtp")

type Credentials interface {
	Credential(account string) (user, password string, ok bool)
}

func SenderAddress(account, user string) string {
	if config.Preset(account) == config.ICloud && !strings.Contains(user, "@") {
		return user + "@" + ICloudDomain
	}
	return user
}

var commaRunRE = rx.MustCompile(`[\s,]*,[\s,]*`, 0)

func SplitAddresses(text string) ([]string, error) {
	text, _ = commaRunRE.Sub(", ", text, 0)
	text = strings.Trim(mime.Strip(text), ",")
	parsed, err := mime.GetAddresses([]string{text})
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, a := range parsed {
		if strings.Contains(a.Address, "@") && !strings.Contains(a.Address, " ") {
			out = append(out, a.Address)
		}
	}
	return out, nil
}

func Send(ctx context.Context, creds Credentials, account string, msg *Message, factory Factory) (Refusals, error) {
	user, password, ok := creds.Credential(account)
	if !ok {
		return nil, SendError(account + " is not configured: no enabled account with a login and password")
	}
	var recipients []string
	for _, h := range [...]string{"To", "Cc"} {
		addrs, err := SplitAddresses(msg.Get(h))
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, addrs...)
	}
	if len(recipients) == 0 {
		return nil, SendError("no recipients")
	}
	if factory == nil {
		factory = Dialer{}.Dial
	}
	refused, err := send(ctx, factory, account, user, password, msg, recipients)
	var unknown *OutcomeUnknown
	if errors.As(err, &unknown) {
		return nil, unknown
	}
	if err != nil {
		return nil, SendError(err.Error())
	}
	return refused, nil
}

func send(ctx context.Context, factory Factory, account, user, password string, msg *Message, recipients []string) (refused Refusals, err error) {
	spec, _ := config.Account(account)
	server, err := factory(ctx, spec.SMTPHost, Port, Timeout)
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
			var unknown *OutcomeUnknown
			if !errors.As(err, &unknown) {
				refused, err = nil, qerr
			}
		}
	}()
	if err := server.StartTLS(); err != nil {
		return nil, err
	}
	if err := server.Login(user, password); err != nil {
		return nil, err
	}
	refused, err = server.SendMessage(msg, SenderAddress(account, user), recipients)
	accepted = err == nil
	return refused, err
}
