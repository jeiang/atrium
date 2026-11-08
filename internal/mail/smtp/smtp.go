package smtp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/mime"
)

const (
	Port         = 587
	Timeout      = 60 * time.Second
	ICloudDomain = "icloud.com"
)

var log = logx.Logger("atrium.smtp")

type Credentials interface {
	Credential(ctx context.Context, account string) (user, password string, ok bool)
}

func SenderAddress(preset, user string) string {
	if preset == config.ICloud && !strings.Contains(user, "@") {
		return user + "@" + ICloudDomain
	}
	return user
}

var commaRunRE = regexp.MustCompile(`[\s,]*,[\s,]*`)

func SplitAddresses(text string) []string {
	text = commaRunRE.ReplaceAllString(text, ", ")
	text = strings.Trim(strings.TrimSpace(text), ",")
	out := []string{}
	for _, a := range mime.ParseAddresses(text) {
		if strings.Contains(a.Address, "@") && !strings.Contains(a.Address, " ") {
			out = append(out, a.Address)
		}
	}
	return out
}

func Send(ctx context.Context, creds Credentials, account string, msg *Message, recipients []string, factory Factory) (Refusals, error) {
	user, password, ok := creds.Credential(ctx, account)
	if !ok {
		return nil, &Failure{Kind: "Config", Msg: account + " is not configured: no enabled account with a login and password"}
	}
	if len(recipients) == 0 {
		return nil, &Failure{Kind: "NoRecipients", Msg: "no recipients"}
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
		return nil, err
	}
	return refused, nil
}

func send(ctx context.Context, factory Factory, account, user, password string, msg *Message, recipients []string) (refused Refusals, err error) {
	spec, _ := config.From(ctx).Account(account)
	server, err := factory(ctx, spec.SMTPHost(), Port, Timeout)
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
	if err := server.Login(user, password); err != nil {
		return nil, err
	}
	refused, err = server.SendMessage(msg, SenderAddress(spec.Preset, user), recipients)
	accepted = err == nil
	return refused, err
}
