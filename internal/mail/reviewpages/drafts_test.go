package reviewpages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/py"
)

func (s *site) mailAccounts() {
	s.exec("INSERT OR IGNORE INTO accounts (name, kind) VALUES ('icloud', 'icloud'), ('gmail', 'gmail')")
	s.exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'box', 'box', 'filed')")
}

func (s *site) original() int64 {
	s.t.Helper()
	s.mailAccounts()
	res, err := s.db.Write.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, first_seen, from_addr, from_name, subject, msgid, to_addrs) " +
		"VALUES ('icloud', 1, 1, 1, 'd', 0, 'friend@x.test', 'Friend', 'Lunch?', '<orig@x.test>', '[\"me@icloud.test\"]')")
	if err != nil {
		s.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *site) draft(account string, to, cc any, subject, body string, replyTo *int64) int64 {
	s.t.Helper()
	var reply any
	if replyTo != nil {
		reply = *replyTo
	}
	id, err := s.store.Create(s.t.Context(), nil, account, to, cc, subject, body, reply)
	if err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *site) row(id int64) drafts.Row {
	s.t.Helper()
	row, err := s.store.Get(s.t.Context(), id)
	if err != nil || row == nil {
		s.t.Fatalf("get draft %d: %v %v", id, row, err)
	}
	return *row
}

func reviewed(row drafts.Row) string { return py.FloatRepr(row.UpdatedAt) }

func TestSendRequiresTheReviewedVersionAndConfirmationIsNotInline(t *testing.T) {
	s := newSite(t)
	id := s.draft(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	page := status(t, s.get("/drafts"), http.StatusOK)
	mustNotContain(t, page, "onsubmit")
	mustContain(t, page, `hx-confirm="Send this email now?"`)
	stale := reviewed(s.row(id))
	if !strings.Contains(page, `name="updated_at" value="`+stale+`"`) || !strings.HasSuffix(stale, ".0") {
		t.Errorf("page misses the Python repr %s of updated_at", stale)
	}
	if _, err := s.store.Update(t.Context(), id, map[string]any{"to": []any{"attacker@x.test"}}); err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("/drafts/%d/send", id)
	mustContain(t, status(t, s.post(target, form("updated_at", stale)), http.StatusConflict), "draft changed, review again")
	status(t, s.post(target, nil), http.StatusConflict)
	status(t, s.post(target, form("updated_at", "0x1p3")), http.StatusConflict)
	if len(s.smtp.Sent) != 0 || s.row(id).Status != drafts.Draft {
		t.Fatalf("a stale send went out: %+v", s.smtp.Sent)
	}
	status(t, s.post(target, form("updated_at", reviewed(s.row(id)))), http.StatusOK)
	if got := s.smtp.Sent[0].To; len(got) != 1 || got[0] != "attacker@x.test" {
		t.Errorf("sent to %v", got)
	}
}

func TestSendICloudUsesSMTPAndFilesTheSentCopy(t *testing.T) {
	s := newSite(t)
	mid := s.original()
	id := s.draft(config.ICloud, []any{"friend@x.test"}, []any{"boss@x.test"}, "Re: Lunch?", "Yes", &mid)
	target := fmt.Sprintf("/drafts/%d/send", id)
	body := status(t, s.post(target, form("updated_at", reviewed(s.row(id))), hx, "true"), http.StatusOK)
	mustContain(t, body, "Sent", "copy filed in Sent", `id="draft-`+fmt.Sprint(id)+`"`, "sent; copy filed in Sent")
	sent := s.smtp.Sent[0]
	if sent.Host != "smtp.mail.me.com" || sent.From != "me@icloud.com" || strings.Join(sent.To, ",") != "friend@x.test,boss@x.test" {
		t.Errorf("sent %+v", sent)
	}
	if len(s.imap.Appended) != 1 || s.imap.Appended[0].Mailbox != "Sent Messages" || !s.imap.Closed {
		t.Errorf("appended %+v closed %v", s.imap.Appended, s.imap.Closed)
	}
	if row := s.row(id); row.Status != drafts.Sent || row.SentAt == nil {
		t.Errorf("row %+v", row)
	}
	status(t, s.post(target, form("updated_at", reviewed(s.row(id)))), http.StatusConflict)
	if len(s.smtp.Sent) != 1 {
		t.Errorf("sent %d times", len(s.smtp.Sent))
	}
}

func TestSendGmailDoesNotAppend(t *testing.T) {
	s := newSite(t)
	id := s.draft(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	status(t, s.post(fmt.Sprintf("/drafts/%d/send", id), form("updated_at", reviewed(s.row(id)))), http.StatusOK)
	if sent := s.smtp.Sent[0]; sent.Host != "smtp.gmail.com" || sent.From != "me@gmail.test" {
		t.Errorf("sent %+v", sent)
	}
	if len(s.imap.Appended) != 0 {
		t.Error("gmail got an appended copy")
	}
}

func TestSendReportsRefusedRecipients(t *testing.T) {
	s := newSite(t)
	s.smtp.Refused = smtp.Refusals{{Addr: "b@x.test", Code: 550, Msg: "no"}}
	id := s.draft(config.Gmail, "a@x.test, b@x.test", nil, "Hi", "body", nil)
	body := status(t, s.post(fmt.Sprintf("/drafts/%d/send", id), form("updated_at", reviewed(s.row(id))), hx, "true"), http.StatusOK)
	mustContain(t, body, "gmail files Sent itself; refused: b@x.test")
}

func TestSMTPFailureRevertsToDraft(t *testing.T) {
	s := newSite(t)
	s.smtp.Fail = func(stage string) error {
		if stage == "login" {
			return &smtp.ResponseError{Kind: "SMTPAuthenticationError", Code: 535, Msg: "bad"}
		}
		return nil
	}
	id := s.draft(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	rec := s.post(fmt.Sprintf("/drafts/%d/send", id), form("updated_at", reviewed(s.row(id))), hx, "true")
	body := status(t, rec, http.StatusConflict)
	mustContain(t, body, "Not sent", "(535, b&#39;bad&#39;)")
	if row := s.row(id); row.Status != drafts.Draft || row.SMTPResult == nil || !strings.Contains(*row.SMTPResult, "failed") {
		t.Errorf("row %+v", row)
	}
	plain := s.post(fmt.Sprintf("/drafts/%d/send", id), form("updated_at", "1"))
	status(t, plain, http.StatusConflict)
}

func TestPagesWarnOnNewRecipientsAndDiscard(t *testing.T) {
	s := newSite(t)
	mid := s.original()
	id := s.draft(config.ICloud, []any{"friend@x.test", "stranger@evil.test"}, nil, "Re", "Yes", &mid)
	page := status(t, s.get("/drafts"), http.StatusOK)
	mustContain(t, page, "stranger@evil.test", "Recipients differ from the original thread", fmt.Sprintf(`href="/messages/%d"`, mid),
		`data-draft-card="`+fmt.Sprint(id)+`"`)
	mustNotContain(t, page, "friend@x.test</strong>")
	status(t, s.get("/drafts?status=sent"), http.StatusOK)
	status(t, s.post(fmt.Sprintf("/drafts/%d/discard", id), nil), http.StatusOK)
	if got := s.row(id).Status; got != drafts.Discarded {
		t.Errorf("status = %s", got)
	}
	again := status(t, s.post(fmt.Sprintf("/drafts/%d/discard", id), nil, hx, "true"), http.StatusConflict)
	mustContain(t, again, "Not discarded")
}

func TestEditFormUpdatesTheDraft(t *testing.T) {
	s := newSite(t)
	id := s.draft(config.ICloud, "a@x.test", nil, "Hi", "body", nil)
	target := fmt.Sprintf("/drafts/%d", id)
	body := status(t, s.post(target, form("account", "gmail", "to", "b@x.test", "cc", "", "subject", "S", "body", "B")), http.StatusOK)
	mustContain(t, body, "Draft updated.", "Saved")
	row := s.row(id)
	if row.Account != "gmail" || row.ToAddrs != "b@x.test" || row.Subject != "S" || row.Body != "B" {
		t.Errorf("row %+v", row)
	}
	fragment := status(t, s.post(target, form("account", "gmail", "to", "", "body", "B"), hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, fragment, "Not saved", "to is required", `<textarea id="body-`+fmt.Sprint(id)+`"`)
	mustNotContain(t, fragment, "<html")
	missing := s.post(target, form("to", "x"))
	if detail := jsonOf(t, status(t, missing, http.StatusUnprocessableEntity))["detail"].([]any)[0].(map[string]any); detail["type"] != "missing" {
		t.Errorf("detail = %v", detail)
	}
	full := status(t, s.post(target, form("account", "gmail", "to", "", "body", "B")), http.StatusUnprocessableEntity)
	mustContain(t, full, "<html", "to is required", "Drafts")
}

func TestBodyAndRecipientsAreVerbatim(t *testing.T) {
	s := newSite(t)
	id := s.draft(config.Gmail, "a@x.test", nil, "Re: <b>", "line one\n\n  <script>x</script>\nline three", nil)
	page := status(t, s.get(fmt.Sprintf("/drafts/%d", id), hx, "true"), http.StatusOK)
	mustContain(t, page, `aria-label="Draft"`, "line one\n\n  &lt;script&gt;x&lt;/script&gt;\nline three</textarea>", `value="Re: &lt;b&gt;"`)
	mustNotContain(t, page, "<script>")
}

func TestUnknownDraftsAreResolvedByTheOperator(t *testing.T) {
	s := newSite(t)
	id := s.draft(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	s.exec("UPDATE drafts SET status = 'unknown', smtp_result = 'send interrupted; outcome unknown' WHERE id = ?", id)
	page := status(t, s.get("/drafts?status=unknown"), http.StatusOK)
	mustContain(t, page, "Check the Sent folder", "Mark sent", "Back to draft", "send interrupted; outcome unknown")
	target := fmt.Sprintf("/drafts/%d/resolve", id)
	status(t, s.post(target, form("outcome", "bogus")), http.StatusUnprocessableEntity)
	status(t, s.post(target, nil), http.StatusUnprocessableEntity)
	body := status(t, s.post(target, form("outcome", "sent"), hx, "true"), http.StatusOK)
	mustContain(t, body, "Draft marked sent.", "Updated")
	if got := s.row(id).Status; got != drafts.Sent {
		t.Errorf("status = %s", got)
	}
	again := status(t, s.post(target, form("outcome", "draft"), hx, "true"), http.StatusConflict)
	mustContain(t, again, "Not updated", "draft is not in the unknown state")
	fullPage := status(t, s.post(target, form("outcome", "draft")), http.StatusConflict)
	mustContain(t, fullPage, "<html", "No unknown drafts")
}

func TestDraftStatusFiltersAndStates(t *testing.T) {
	s := newSite(t)
	status(t, s.get("/drafts?status=bogus"), http.StatusUnprocessableEntity)
	status(t, s.get("/drafts?status=sending"), http.StatusOK)
	mustContain(t, status(t, s.get("/drafts"), http.StatusOK), "No draft drafts")
	mustContain(t, status(t, s.get("/drafts?status="), http.StatusOK), "No  drafts", "Ask the assistant in chat to draft a reply.")
	for _, state := range []string{"discarded", "sending"} {
		id := s.draft(config.Gmail, "a@x.test", nil, "Hi "+state, "body "+state, nil)
		s.exec("UPDATE drafts SET status = ? WHERE id = ?", state, id)
		page := status(t, s.get(fmt.Sprintf("/drafts/%d", id)), http.StatusOK)
		mustContain(t, page, "Hi "+state, "body "+state)
		mustNotContain(t, page, `action="/drafts/`+fmt.Sprint(id)+`/send"`, "Save changes", "Check the Sent folder")
	}
	status(t, s.get("/drafts/999"), http.StatusNotFound)
	status(t, s.get("/drafts/999", hx, "true"), http.StatusNotFound)
	status(t, s.post("/drafts/999/discard", nil, hx, "true"), http.StatusNotFound)
}

func TestHTMXCardResponsesCarryTheToastAfterTheRow(t *testing.T) {
	s := newSite(t)
	id := s.draft(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	body := status(t, s.post(fmt.Sprintf("/drafts/%d/discard", id), nil, hx, "true"), http.StatusOK)
	if strings.Index(body, "</li>") > strings.Index(body, `id="toaster"`) || strings.Count(body, `id="toaster"`) != 1 {
		t.Errorf("toast placement: %s", body)
	}
	full := status(t, s.get("/drafts?status=discarded"), http.StatusOK)
	if strings.Count(full, `id="toaster"`) != 1 {
		t.Errorf("a full page has %d toasters", strings.Count(full, `id="toaster"`))
	}
}

func TestAnAccountWithoutCredentialsIsMarkedNotConfigured(t *testing.T) {
	s := newSite(t, func(env map[string]string) { delete(env, "GMAIL_ADDRESS") })
	s.draft(config.ICloud, "a@x.test", nil, "Hi", "body", nil)
	page := status(t, s.get("/drafts"), http.StatusOK)
	mustContain(t, page, ">gmail (not configured)</option>")
	mustNotContain(t, page, ">icloud (not configured)</option>")
}
