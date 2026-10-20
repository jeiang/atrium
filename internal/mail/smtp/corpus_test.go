package smtp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/mail/smtp/smtptest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type creds struct{ account, user string }

func (c creds) Credential(account string) (string, string, bool) {
	if account == c.account && c.user != "" {
		return c.user, "app-password", true
	}
	return "", "", false
}

func asJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decode(t *testing.T, raw json.RawMessage, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func TestCorpus(t *testing.T) {
	c := smtptest.LoadCorpus(t)
	counts := map[string]int{}
	for i, r := range c.Records {
		counts[r.Fn]++
		if strings.HasPrefix(r.Fn, "drafts.") {
			continue
		}
		t.Run(fmt.Sprintf("%02d_%s_%s", i, r.Fn, r.Case), func(t *testing.T) {
			switch r.Fn {
			case "sender_address":
				var in []string
				decode(t, r.Input, &in)
				golden.EqualJSON(t, r.Output, asJSON(t, smtp.SenderAddress(in[0], in[1])))
			case "split_addresses":
				var in []*string
				decode(t, r.Input, &in)
				text := ""
				if in[0] != nil {
					text = *in[0]
				}
				got, err := smtp.SplitAddresses(text)
				if err != nil {
					t.Fatal(err)
				}
				golden.EqualJSON(t, r.Output, asJSON(t, got))
			case "build_message":
				var in []json.RawMessage
				var sender, asString string
				decode(t, r.Input, &in)
				decode(t, in[0], &sender)
				decode(t, r.Output, &asString)
				msg := smtp.BuildMessage(sender, c.Drafts[r.Case], c.Now, smtptest.PinnedMsgID(smtptest.SenderDomain(sender)))
				for _, utf8 := range []bool{false, true} {
					want := r.AsBytes
					if utf8 {
						want = r.AsBytesUTF8
					}
					got, err := msg.Bytes("\n", utf8)
					if err != nil {
						t.Fatal(err)
					}
					if string(got) != string(want) {
						t.Errorf("utf8=%v: message differs\n got: %q\nwant: %q", utf8, got, want)
					}
				}
				if msg.Get("Content-Transfer-Encoding") != "8bit" && string(r.AsBytes) != asString {
					t.Errorf("as_string and as_bytes differ for a %s body", msg.Get("Content-Transfer-Encoding"))
				}
			case "send":
				user := r.User
				if user == "" {
					user = "x"
				}
				sender := smtp.SenderAddress(r.Account, user)
				msg := smtp.BuildMessage(sender, c.Drafts[r.Case], c.Now, smtptest.PinnedMsgID(smtptest.SenderDomain(sender)))
				server := smtptest.NewServer(t, r)
				refused, err := smtp.Send(t.Context(), creds{r.Account, r.User}, r.Account, msg, server.Factory)
				if r.Error != nil {
					var serr smtp.SendError
					if !errors.As(err, &serr) || r.Error.Type != "SendError" || serr.Error() != r.Error.Message {
						t.Errorf("got %v (%T), want %s(%q)", err, err, r.Error.Type, r.Error.Message)
					}
				} else if err != nil {
					t.Fatal(err)
				} else {
					golden.EqualJSON(t, r.Output, asJSON(t, smtptest.RefusalMap(refused)))
				}
				calls := server.Calls
				if calls == nil {
					calls = []smtptest.Call{}
				}
				golden.EqualJSON(t, r.Calls, asJSON(t, calls))
			default:
				t.Fatalf("unknown fn %q", r.Fn)
			}
		})
	}
	for _, fn := range []string{"sender_address", "split_addresses", "build_message", "send", "drafts.send", "drafts.normalize_addresses"} {
		if counts[fn] == 0 {
			t.Errorf("no %s records", fn)
		}
	}
}
