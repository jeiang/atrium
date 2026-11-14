package drafts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/gmailauth"
	"github.com/jeiang/atrium/internal/mail/mailconn"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/safety"
)

type Filed int

const (
	FiledNone Filed = iota
	FiledCopy
	FiledByServer
	FiledNoMailbox
	FiledFailed
)

func (f Filed) String() string {
	switch f {
	case FiledCopy:
		return "copy filed in Sent"
	case FiledByServer:
		return "gmail files Sent itself"
	case FiledNoMailbox:
		return "no Sent mailbox found; copy not filed"
	case FiledFailed:
		return "Sent copy failed"
	}
	return ""
}

type Result struct {
	Refused    smtp.Refusals
	Filed      Filed
	FiledError string
	EffectIDs  []int64 `json:"-"`
	Stale      bool    `json:"-"`
}

func (s *Store) fileSentCopy(ctx context.Context, account string, msg *smtp.Message) (filed Filed, effectID int64, err error) {
	if config.From(ctx).Preset(account) != config.ICloud {
		return FiledByServer, 0, nil
	}
	if s.Open == nil {
		return FiledFailed, 0, errors.New("no IMAP opener")
	}
	client, err := s.Open(ctx, mail.OpenOptions{Account: account, Q: s.DB.Read, SentOnly: true})
	if err != nil {
		return FiledFailed, 0, err
	}
	defer client.Close()
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return FiledFailed, 0, err
	}
	sent := ""
	for _, b := range boxes {
		if mail.MailboxRole(b.Name, b.Flags, config.From(ctx).Preset(account)) == "sent" {
			sent = b.IMAPName
			break
		}
	}
	if sent == "" {
		return FiledNoMailbox, 0, nil
	}
	msgid := mime.ParseMsgID(msg.Get("Message-ID")).Bare()
	store := s.ledgerStore()
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: "operator_send", Operation: AppendOperation, OperationKey: AppendOperation + ":" + msgid,
		Target: fmt.Sprintf("mail:%s:%s", account, sent), Account: account, Dest: sent,
		Action:    jsonOf(map[string]any{"account": account, "imap_name": sent, "msgid": msgid}),
		Resources: []ledger.Resource{{Key: "mail_folder:" + account + ":" + sent}},
	}, nil)
	if err != nil {
		return FiledFailed, 0, err
	}
	raw := msg.Bytes("\r\n", false)
	if err := client.Append(safety.OperatorSend(ctx, sent), sent, raw, `\Seen`); err != nil {
		state := ledger.Failed
		text := err.Error()
		if _, unknown := errors.AsType[*mail.WriteInterrupted](err); unknown {
			state = ledger.Unknown
			text = "the copy in Sent may exist: check Sent before filing it again"
		}
		_ = store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: state, Error: text}, nil)
		return FiledFailed, ticket.ID, errors.New(text)
	}
	if err := store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, After: jsonOf(map[string]any{"account": account, "imap_name": sent, "msgid": msgid}),
	}, nil); err != nil {
		return FiledFailed, ticket.ID, err
	}
	return FiledCopy, ticket.ID, nil
}

func (s *Store) abandon(ctx context.Context, id int64, cause error) {
	if err := s.transition(ctx, id, Sending, Draft, func(u *updateSet) {
		u.set("smtp_result", "send failed: "+cause.Error())
	}); err != nil {
		log.Warn(fmt.Sprintf("draft %d could not be reset after a failed send: %s", id, err))
	}
}

func (s *Store) Send(ctx context.Context, id int64, reviewedRevision int64) (out Result, err error) {
	var draft *Row
	var ticket ledger.Ticket
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE drafts SET status = ?, updated_at = ?, revision = revision + 1 WHERE id = ? AND status = ? AND revision = ?",
			string(Sending), s.now(), id, string(Draft), reviewedRevision)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotSendable
		}
		row, err := scanRow(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM drafts WHERE id = ?", id))
		if err != nil {
			return err
		}
		draft = &row
		ticket, err = s.ledgerStore().BeginTx(ctx, tx, ledger.New{
			Origin: "operator_send", Operation: SendOperation,
			OperationKey: fmt.Sprintf("draft:%d:%d", id, reviewedRevision),
			Target:       SendTarget(row.Account, id), Account: row.Account, Src: string(Draft), Dest: string(Sending),
			Action:    jsonOf(map[string]any{"draft_id": id, "revision": reviewedRevision}),
			Resources: []ledger.Resource{{Key: fmt.Sprintf("mail:draft:%d", id)}},
		}, nil)
		return err
	})
	if err != nil {
		if errors.Is(err, errNotSendable) {
			current, gerr := s.Get(ctx, id)
			if gerr != nil {
				return Result{}, gerr
			}
			if current != nil && current.Status == Draft {
				return Result{Stale: true}, Error("draft changed, review again")
			}
			return Result{}, Error("draft is not in a sendable state")
		}
		return Result{}, err
	}
	out.EffectIDs = []int64{ticket.ID}
	msg, refused, err := s.submit(ctx, draft)
	var unknown *smtp.OutcomeUnknown
	if errors.As(err, &unknown) {
		text := "send outcome unknown: " + unknown.Error()
		if uerr := s.settle(ctx, ticket, ledger.Result{State: ledger.Unknown, Error: text}, func(ctx context.Context, tx *sql.Tx) error {
			return transitionTx(ctx, tx, s.now(), id, Sending, Unknown, func(u *updateSet) { u.set("smtp_result", text) })
		}); uerr != nil {
			return out, uerr
		}
		return out, OutcomeUnknown("the mail may have been sent (" + unknown.Error() + "); check before you choose Back to draft")
	}
	if err != nil {
		text := "send failed: " + err.Error()
		if uerr := s.settle(ctx, ticket, ledger.Result{State: ledger.Failed, Error: text}, func(ctx context.Context, tx *sql.Tx) error {
			return transitionTx(ctx, tx, s.now(), id, Sending, Draft, func(u *updateSet) { u.set("smtp_result", text) })
		}); uerr != nil {
			return out, uerr
		}
		return out, err
	}
	result := "sent"
	if len(refused) > 0 {
		result += "; refused: " + refusalsJSON(refused)
	}
	msgid := mime.ParseMsgID(msg.Get("Message-ID")).Bare()
	if err := s.settle(ctx, ticket, ledger.Result{State: ledger.Done,
		After: jsonOf(map[string]any{"draft_id": id, "msgid": msgid})}, func(ctx context.Context, tx *sql.Tx) error {
		return transitionTx(ctx, tx, s.now(), id, Sending, Sent, func(u *updateSet) {
			u.set("sent_at", s.now())
			u.set("smtp_result", result)
			u.set("sent_msgid", msgid)
		})
	}); err != nil {
		return out, err
	}
	filed, appendID, ferr := s.fileSentCopy(ctx, draft.Account, msg)
	if appendID != 0 {
		out.EffectIDs = append(out.EffectIDs, appendID)
	}
	filedError := ""
	if ferr != nil {
		log.Warn(fmt.Sprintf("sent copy for draft %d not filed: %s", id, ferr))
		filed = FiledFailed
		filedError = ferr.Error()
	}
	detail := filed.String()
	if filedError != "" {
		detail += ": " + filedError
	}
	out.Refused, out.Filed, out.FiledError = refused, filed, filedError
	if _, err := s.exec(ctx, "UPDATE drafts SET smtp_result = ? WHERE id = ? AND status = ?", result+"; "+detail, id, string(Sent)); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) settle(ctx context.Context, ticket ledger.Ticket, result ledger.Result, local ledger.Mutation) error {
	return s.ledgerStore().Settle(ctx, ticket, result, local)
}

func jsonOf(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

var errNotSendable = errors.New("draft is not sendable")

func (s *Store) submit(ctx context.Context, d *Row) (*smtp.Message, smtp.Refusals, error) {
	spec, known := config.From(ctx).Account(d.Account)
	if !known {
		return nil, nil, &smtp.Failure{Kind: "Config", Msg: d.Account + " is not configured"}
	}
	lease, err := mailconn.Open(ctx, s.DB.Read, d.Account)
	if err != nil {
		return nil, nil, err
	}
	host := mailconn.SMTPHost(lease, spec)
	method, source, err := gmailauth.Resolve(ctx, s.DB.Read, d.Account)
	if err != nil {
		return nil, nil, err
	}
	user, _, ok := config.Credential(ctx, d.Account)
	if !ok && method != config.AuthXOAUTH2 {
		return nil, nil, &smtp.Failure{Kind: "Config", Msg: d.Account + " is not configured"}
	}
	if user == "" {
		user = spec.Login
	}
	sender := smtp.SenderAddress(spec.Preset, user)
	makeID := s.MsgID
	if makeID == nil {
		makeID = smtp.MakeMsgID
	}
	domain := sender[strings.LastIndex(sender, "@")+1:]
	msg := smtp.BuildMessage(sender, smtp.Fields{
		ToAddrs: d.ToAddrs, CcAddrs: d.CcAddrs, Subject: d.Subject, Body: d.Body, InReplyTo: d.InReplyTo, Refs: d.Refs,
	}, s.now(), makeID(domain))
	recipients := append(addresses(d.ToAddrs), addresses(d.CcAddrs)...)
	var refused smtp.Refusals
	if method == config.AuthXOAUTH2 {
		refused, err = smtp.SendXOAUTH2(ctx, host, d.Account, user, msg, recipients, source.Token, source.Invalidate)
	} else {
		refused, err = smtp.Send(ctx, host, config.Credential, d.Account, msg, recipients)
	}
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
