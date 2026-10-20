package drafts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/safety"
)

type Result struct {
	Refused smtp.Refusals
	Filed   string
}

func (s *Store) fileSentCopy(ctx context.Context, account string, msg *smtp.Message) (string, error) {
	if account != config.ICloud {
		return "gmail files Sent itself", nil
	}
	if s.Open == nil {
		return "", errors.New("no IMAP opener")
	}
	client, err := s.Open(ctx, account)
	if err != nil {
		return "", err
	}
	defer client.Close()
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return "", err
	}
	sent := ""
	for _, b := range boxes {
		if mail.MailboxRole(b.Name, b.Flags, account) == "sent" {
			sent = b.IMAPName
			break
		}
	}
	if sent == "" {
		return "no Sent mailbox found; copy not filed", nil
	}
	raw, err := msg.Bytes("\r\n", false)
	if err != nil {
		return "", err
	}
	if err := client.Append(safety.OperatorSend(ctx, sent), sent, raw, `\Seen`); err != nil {
		if _, unknown := errors.AsType[*mail.WriteInterrupted](err); unknown {
			return "", errors.New("the copy in Sent may exist: check Sent before filing it again")
		}
		return "", err
	}
	return "copy filed in Sent", nil
}

func stripMsgID(id string) string {
	out, _ := msgidJunkRE.Sub("", id, 0)
	return out
}

func (s *Store) Send(ctx context.Context, id int64, reviewedAt float64) (Result, error) {
	n, err := s.exec(ctx, "UPDATE drafts SET status = ?, updated_at = ? WHERE id = ? AND status = ? AND updated_at = ?",
		Sending, s.now(), id, Draft, reviewedAt)
	if err != nil {
		return Result{}, err
	}
	if n == 0 {
		current, err := s.Get(ctx, id)
		if err != nil {
			return Result{}, err
		}
		if current != nil && current.Status == Draft {
			return Result{}, Error("draft changed, review again")
		}
		return Result{}, Error("draft is not in a sendable state")
	}
	draft, err := s.Get(ctx, id)
	if err != nil {
		return Result{}, err
	}
	msg, refused, err := s.submit(ctx, draft)
	var unknown *smtp.OutcomeUnknown
	if errors.As(err, &unknown) {
		if _, uerr := s.exec(ctx, "UPDATE drafts SET status = ?, smtp_result = ?, updated_at = ? WHERE id = ?",
			Unknown, "send outcome unknown: "+unknown.Error(), s.now(), id); uerr != nil {
			return Result{}, uerr
		}
		return Result{}, OutcomeUnknown("the mail may have been sent (" + unknown.Error() + "); check before you choose Back to draft")
	}
	var serr smtp.SendError
	if errors.As(err, &serr) {
		if _, uerr := s.exec(ctx, "UPDATE drafts SET status = ?, smtp_result = ?, updated_at = ? WHERE id = ?",
			Draft, "send failed: "+serr.Error(), s.now(), id); uerr != nil {
			return Result{}, uerr
		}
		return Result{}, serr
	}
	if err != nil {
		return Result{}, err
	}
	result := "sent"
	if len(refused) > 0 {
		result += "; refused: " + refusalsJSON(refused)
	}
	if _, err := s.exec(ctx, "UPDATE drafts SET status = ?, sent_at = ?, smtp_result = ?, updated_at = ?, sent_msgid = ? WHERE id = ?",
		Sent, s.now(), result, s.now(), stripMsgID(msg.Get("Message-ID")), id); err != nil {
		return Result{}, err
	}
	filed, err := s.fileSentCopy(ctx, draft.Account, msg)
	if err != nil {
		log.Warn(fmt.Sprintf("sent copy for draft %d not filed: %s", id, err))
		filed = "Sent copy failed: " + err.Error()
	}
	if _, err := s.exec(ctx, "UPDATE drafts SET smtp_result = ? WHERE id = ?", result+"; "+filed, id); err != nil {
		return Result{}, err
	}
	return Result{Refused: refused, Filed: filed}, nil
}

func (s *Store) submit(ctx context.Context, d *Row) (*smtp.Message, smtp.Refusals, error) {
	user, _, ok := s.Cfg.Credential(d.Account)
	if !ok {
		return nil, nil, smtp.SendError(d.Account + " is not configured")
	}
	sender := smtp.SenderAddress(d.Account, user)
	makeID := s.MsgID
	if makeID == nil {
		makeID = smtp.MakeMsgID
	}
	domain := sender[strings.LastIndex(sender, "@")+1:]
	msg := smtp.BuildMessage(sender, smtp.Fields{
		ToAddrs: d.ToAddrs, CcAddrs: d.CcAddrs, Subject: d.Subject, Body: d.Body, InReplyTo: d.InReplyTo, Refs: d.Refs,
	}, s.now(), makeID(domain))
	refused, err := smtp.Send(ctx, s.Cfg, d.Account, msg, s.SMTP)
	return msg, refused, err
}

func refusalsJSON(rs smtp.Refusals) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = pyJSONString(r.Addr) + ": " + pyJSONString(r.Reply())
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func pyJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || r >= 0x7f && r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r >= 0x10000:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
