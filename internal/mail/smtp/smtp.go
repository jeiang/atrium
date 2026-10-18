// Package smtp is the operator's draft send (src/atrium/smtp.py): STARTTLS submission on port 587 with the IMAP
// credentials, smtplib's per-recipient refusal semantics and its error wording.
package smtp

import (
	"context"
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

// Hosts is SMTP_HOSTS.
var Hosts = map[string]string{config.ICloud: "smtp.mail.me.com", config.Gmail: "smtp.gmail.com"}

var log = logx.Logger("atrium.smtp")

// Credentials is what smtp.send reads from the config: the IMAP login of an account.
type Credentials interface {
	Credential(account string) (user, password string, ok bool)
}

func SenderAddress(account, user string) string {
	if account == config.ICloud && !strings.Contains(user, "@") {
		return user + "@" + ICloudDomain
	}
	return user
}

var commaRunRE = rx.MustCompile(`[\s,]*,[\s,]*`, 0)

// SplitAddresses is smtp.split_addresses: the bare addresses of a To/Cc value, skipping empty list entries and
// anything without an @ or with a space. The error is the RecursionError of a too deeply nested value.
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

// Send is smtp.send: it returns the recipients the server refused (empty on full success) and fails with a
// SendError when nothing was sent.
func Send(ctx context.Context, creds Credentials, account string, msg *Message, factory Factory) (Refusals, error) {
	user, password, ok := creds.Credential(account)
	if !ok {
		spec, _ := config.Account(account)
		return nil, SendError(fmt.Sprintf("%s is not configured: %s/%s missing", account, spec.UserEnv, spec.PasswordEnv))
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
	if err != nil {
		return nil, SendError(err.Error())
	}
	return refused, nil
}

func send(ctx context.Context, factory Factory, account, user, password string, msg *Message, recipients []string) (refused Refusals, err error) {
	server, err := factory(ctx, Hosts[account], Port, Timeout)
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
			refused, err = nil, qerr
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
