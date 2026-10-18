package drafts_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/mail/smtp/smtptest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func asJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func errorType(err error) string {
	var derr drafts.Error
	var serr smtp.SendError
	var failure *smtp.Failure
	switch {
	case errors.As(err, &derr):
		return "DraftError"
	case errors.As(err, &serr):
		return "SendError"
	case errors.As(err, &failure):
		return failure.Kind
	}
	return fmt.Sprintf("%T", err)
}

func checkError(t *testing.T, want *smtptest.PyError, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s(%q)", want.Type, want.Message)
	}
	if got := errorType(err); got != want.Type || err.Error() != want.Message {
		t.Errorf("got %s(%q), want %s(%q)", got, err.Error(), want.Type, want.Message)
	}
}

// TestCorpusDrafts runs the drafts.py records of the SMTP corpus: normalize_addresses and the whole operator
// send against the fake SMTP server and a fake iCloud mailbox in shadow mode.
func TestCorpusDrafts(t *testing.T) {
	c := smtptest.LoadCorpus(t)
	n := 0
	for i, r := range c.Records {
		switch r.Fn {
		case "drafts.normalize_addresses":
			n++
			t.Run(fmt.Sprintf("%02d_normalize", i), func(t *testing.T) {
				var in []any
				if err := json.Unmarshal(r.Input, &in); err != nil {
					t.Fatal(err)
				}
				got, err := drafts.NormalizeAddresses(in[0].(string), in[1])
				if r.Error != nil {
					checkError(t, r.Error, err)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				golden.EqualJSON(t, r.Output, asJSON(t, got))
			})
		case "drafts.send":
			n++
			t.Run(fmt.Sprintf("%02d_send_%s", i, r.Case), func(t *testing.T) { runDraftSend(t, c, r) })
		}
	}
	if n == 0 {
		t.Fatal("no drafts records")
	}
}

func runDraftSend(t *testing.T, c smtptest.Corpus, r smtptest.Record) {
	credentials := map[string]string{}
	if r.User != "" {
		spec, _ := config.Account(r.Account)
		credentials[spec.UserEnv] = r.User
		credentials[spec.PasswordEnv] = "app-password"
	}
	w := newWorldOn(t, dbtest.Schema(t), credentials)
	w.smtp = smtptest.NewServer(t, r)
	w.store.SMTP = w.smtp.Factory
	w.store.MsgID = smtptest.PinnedMsgID
	w.store.Now = func() float64 { return c.Now }
	w.imap = mailtest.NewFakeClient(config.Map(map[string]string{}))
	w.imap.AddBox("INBOX", mailtest.NewBox(1))
	if r.SentMailbox != nil {
		w.imap.AddBox(*r.SentMailbox, mailtest.NewBox(1), `\Sent`)
	}
	if r.AppendError != "" {
		w.imap.FailWrite = func(mailtest.FakeWrite) error { return errors.New(r.AppendError) }
	}
	d := c.Drafts[r.Case]
	w.exec("INSERT INTO drafts (id, account, to_addrs, cc_addrs, subject, body, in_reply_to, refs, created_at, updated_at) "+
		"VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)", r.Account, d.ToAddrs, d.CcAddrs, d.Subject, d.Body, d.InReplyTo, d.Refs, c.Now, c.Now)

	result, err := w.store.Send(t.Context(), 1, c.Now)
	if r.Error != nil {
		checkError(t, r.Error, err)
	} else {
		if err != nil {
			t.Fatal(err)
		}
		golden.EqualJSON(t, r.Output, asJSON(t, map[string]any{"filed": result.Filed, "refused": smtptest.RefusalMap(result.Refused)}))
	}
	row := w.get(1)
	golden.EqualJSON(t, r.Row, asJSON(t, map[string]any{"status": row.Status, "smtp_result": row.SMTPResult, "sent_msgid": row.SentMsgID}))
	appended := []string{}
	for _, a := range w.imap.Appended {
		appended = append(appended, a.Mailbox)
	}
	golden.EqualJSON(t, asJSON(t, r.AppendedTo), asJSON(t, appended))
}
