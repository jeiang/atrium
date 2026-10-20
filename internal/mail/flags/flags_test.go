package flags

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

const vcard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Pal\r\nitem1.EMAIL;type=INTERNET;type=HOME:Pal@Friends.Test\r\n" +
	"EMAIL:other@\r\n bar.test\r\nEND:VCARD\r\n"

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{"src/atrium/flags.py:AUTOMATED_LOCAL": automatedLocal})
}

func contactsDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	book := filepath.Join(root, "contacts", "book")
	if err := os.MkdirAll(book, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(book, "a.vcf"), []byte(vcard), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "contacts")
}

type decisionRow struct {
	contacts int
	llm      *int
	person   *int
}

func readDecisions(t *testing.T, d *db.DB) []decisionRow {
	t.Helper()
	rows, err := d.Read.QueryContext(t.Context(), "SELECT contacts_match, llm_flag, person_asking FROM flag_decisions ORDER BY message_id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []decisionRow
	for rows.Next() {
		var r decisionRow
		if err := rows.Scan(&r.contacts, &r.llm, &r.person); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestFlagAccountRecordsBothSignalsAndDefersLLM(t *testing.T) {
	d := rulestest.DB(t)
	cfg := config.Config{ContactsDir: contactsDir(t), ChatURL: "http://chat.invalid/v1"}
	rulestest.Inbox(t, d, 1, "pal@friends.test", "friends.test", nil, rulestest.Fields{"seen": 0})
	rulestest.Inbox(t, d, 2, "shop@shop.test", "shop.test", nil, rulestest.Fields{"seen": 0})
	rulestest.Inbox(t, d, 3, "read@shop.test", "shop.test", nil, rulestest.Fields{"seen": 1})
	ctx := t.Context()
	down := func(context.Context, string, llm.Message, string) (llm.Flags, error) {
		return llm.Flags{}, &httpx.ServiceUnavailable{Msg: "down"}
	}
	n, err := FlagAccount(ctx, d, cfg, "icloud", down, nil)
	if err != nil || n != 2 {
		t.Fatalf("FlagAccount = %d, %v", n, err)
	}
	got := readDecisions(t, d)
	if len(got) != 2 || got[0].contacts != 1 || got[1].contacts != 0 || got[0].llm != nil || got[1].llm != nil {
		t.Errorf("deferred rows = %+v", got)
	}
	verdict := llm.Flags{PersonAsking: true, Flag: true}
	judge := func(context.Context, string, llm.Message, string) (llm.Flags, error) { return verdict, nil }
	if n, err := FlagAccount(ctx, d, cfg, "icloud", judge, nil); err != nil || n != 2 {
		t.Fatalf("second pass = %d, %v", n, err)
	}
	got = readDecisions(t, d)
	if len(got) != 2 || got[0].llm == nil || *got[0].llm != 1 || *got[1].llm != 1 || *got[0].person != 1 || got[1].contacts != 0 {
		t.Errorf("judged rows = %+v", got)
	}
	if n, err := FlagAccount(ctx, d, cfg, "icloud", judge, nil); err != nil || n != 0 {
		t.Errorf("third pass = %d, %v", n, err)
	}
}

func TestFlagAccountStopsJudgingAfterOutage(t *testing.T) {
	d := rulestest.DB(t)
	for uid := 1; uid <= 3; uid++ {
		rulestest.Inbox(t, d, uid, fmt.Sprintf("s%d@x.test", uid), "x.test", nil, rulestest.Fields{"seen": 0})
	}
	calls := 0
	judge := func(context.Context, string, llm.Message, string) (llm.Flags, error) {
		calls++
		if calls == 2 {
			return llm.Flags{}, &httpx.ServiceUnavailable{Msg: "down"}
		}
		return llm.Flags{Flag: true}, nil
	}
	if n, err := FlagAccount(t.Context(), d, config.Config{}, "icloud", judge, nil); err != nil || n != 3 || calls != 2 {
		t.Fatalf("FlagAccount = %d, %v after %d calls", n, err, calls)
	}
	got := readDecisions(t, d)
	if got[0].llm == nil || got[1].llm != nil || got[2].llm != nil {
		t.Errorf("rows = %+v", got)
	}
}

func TestFlagAccountPropagatesMalformedVerdict(t *testing.T) {
	d := rulestest.DB(t)
	rulestest.Inbox(t, d, 1, "a@x.test", "x.test", nil, rulestest.Fields{"seen": 0})
	bad := fmt.Errorf("%w: chat reply content is not JSON: %q", httpx.ErrValue, "nope")
	judge := func(context.Context, string, llm.Message, string) (llm.Flags, error) { return llm.Flags{}, bad }
	n, err := FlagAccount(t.Context(), d, config.Config{}, "icloud", judge, nil)
	if !errors.Is(err, httpx.ErrValue) || n != 0 {
		t.Fatalf("FlagAccount = %d, %v", n, err)
	}
	if got := readDecisions(t, d); len(got) != 0 {
		t.Errorf("rows = %+v", got)
	}
}

func sorted(c Contacts) []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestVCardEmailsHandlesGroupsParamsAndFolding(t *testing.T) {
	if got := sorted(VCardEmails(vcard)); !slices.Equal(got, []string{"other@bar.test", "pal@friends.test"}) {
		t.Errorf("got %v", got)
	}
}

func TestVCardEmailsHandlesDottedParams(t *testing.T) {
	text := "BEGIN:VCARD\r\nEMAIL;TYPE=a.b:x@y.test\r\nitem2.EMAIL;X-LABEL=v1.2:z@y.test\r\nEND:VCARD\r\n"
	if got := sorted(VCardEmails(text)); !slices.Equal(got, []string{"x@y.test", "z@y.test"}) {
		t.Errorf("got %v", got)
	}
}

func TestLoadContactsMissingDirectory(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "absent")} {
		if got := LoadContacts(dir); len(got) != 0 {
			t.Errorf("LoadContacts(%q) = %v", dir, got)
		}
	}
}

func TestContactsRuleConditions(t *testing.T) {
	known := LoadContacts(contactsDir(t))
	check := func(addr, listID string) bool {
		got, err := ContactsFlag(Row{FromAddr: addr, ListID: listID}, known)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if !check("pal@friends.test", "") {
		t.Error("a contact is flagged")
	}
	if check("stranger@friends.test", "") {
		t.Error("a stranger is not")
	}
	if check("other@bar.test", "<l>") {
		t.Error("a list message is not")
	}
	known["noreply@shop.test"] = struct{}{}
	known["notifications@shop.test"] = struct{}{}
	for _, addr := range []string{"noreply@shop.test", "notifications@shop.test"} {
		if check(addr, "") {
			t.Errorf("%s is automated", addr)
		}
	}
}

func TestFlagJudgmentSchemaFields(t *testing.T) {
	want := []string{"person_asking", "deadline_or_payment_due", "security_event", "flag"}
	if !slices.Equal(llm.FlagFields[:], want) {
		t.Errorf("FlagFields = %v", llm.FlagFields)
	}
}
