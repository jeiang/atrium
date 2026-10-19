package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

func TestMessagePageFullPartialAndMissing(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	path := "/messages/" + s.id(s.ids.invoice)
	contains(t, status(t, s.get(path), 200, "full"), "<html")
	partial := status(t, s.get(path, hx), 200, "partial")
	contains(t, partial, "invoice due friday")
	lacks(t, partial, "<html")
	missing := status(t, s.get("/messages/9999"), 404, "missing")
	contains(t, missing, "<html", "Nothing here")
	lacks(t, status(t, s.get("/messages/9999", hx), 404, "missing partial"), "<html")
	contains(t, status(t, s.get(path), 200, "chat back"), `href="/chat"`)
	contains(t, status(t, s.get(path, [2]string{"Referer", "http://testserver/triage?stage=operator"}), 200, "triage back"),
		`href="/triage?stage=operator"`, "</svg>Decisions</a>")
	lacks(t, status(t, s.get(path, [2]string{"Referer", "http://elsewhere.test/triage"}), 200, "foreign"), "</svg>Decisions</a>")
	status(t, s.get("/messages/abc"), 422, "bad id")
}

func TestMessagePageLinksBackToItsDraftThread(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	rulestest := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Write.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	rulestest("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (7, 'chat', 'medium', 1, 1)")
	rulestest("UPDATE messages SET msgid = 'abc@me.test' WHERE id = ?", s.ids.other)
	rulestest("INSERT INTO drafts (thread_id, account, to_addrs, subject, body, status, sent_msgid, created_at, updated_at) " +
		"VALUES (7, 'icloud', 'x@y.test', 'Sent one', 'body', 'sent', 'abc@me.test', 1, 1)")
	contains(t, status(t, s.get("/messages/"+s.id(s.ids.other)), 200, "message"), `href="/chat/7"`, "From thread")
	lacks(t, status(t, s.get("/messages/"+s.id(s.ids.invoice)), 200, "other"), "From thread")
}

func TestCorrectFormRendersAccountFolders(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	path := "/messages/" + s.id(s.ids.invoice) + "/correct"
	contains(t, status(t, s.get(path+"?context=row"), 200, "form"), "Bills")
	if got := mustJSON(t, status(t, s.get(path+"?context=bogus"), 422, "bogus")); got["detail"] == nil {
		t.Errorf("422 body = %v", got)
	}
	status(t, s.get("/messages/9999/correct"), 404, "unknown")
	contains(t, status(t, s.get(path), 200, "default"), `id="dest-message-`)
}

func TestCorrectionFromRowRecordsWithoutMailboxWrite(t *testing.T) {
	opened := 0
	s := seeded(t, "shadow", Options{Open: func(_ context.Context, _ string) (mail.Client, error) {
		opened++
		return nil, errors.New("no mailbox in shadow mode")
	}})
	body := status(t, s.post("/messages/"+s.id(s.ids.invoice)+"/correct", url.Values{"dest": {"Bills"}, "context": {"row"}}, hx), 200, "row")
	contains(t, body, `id="decision-`+s.id(s.ids.invoice)+`"`, "Correction recorded", `id="toaster"`)
	if got := s.column("SELECT message_id || ':' || folder FROM corrections"); len(got) != 1 || got[0] != s.id(s.ids.invoice)+":Bills" {
		t.Errorf("corrections = %v", got)
	}
	if opened != 0 {
		t.Errorf("the mailbox was opened %d times in shadow mode", opened)
	}
}

func TestCorrectionFromMessageCardAndErrors(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	path := "/messages/" + s.id(s.ids.other) + "/correct"
	bad := status(t, s.post(path, url.Values{"dest": {"INBOX"}, "context": {"message"}}, hx), 200, "bad")
	contains(t, bad, "invalid destination", `data-invalid`, `aria-invalid="true"`)
	if n := s.column("SELECT COUNT(*) FROM corrections"); n[0] != "0" {
		t.Errorf("a refused correction was stored: %v", n)
	}
	ok := status(t, s.post(path, url.Values{"dest": {"Bills"}, "context": {"message"}}, hx), 200, "ok")
	contains(t, ok, "Correction recorded", "Bills", `role="status"`)
	lacks(t, ok, `id="toaster"`)
	status(t, s.post("/messages/9999/correct", url.Values{"dest": {"Bills"}}, hx), 404, "unknown")
	status(t, s.post(path, url.Values{"dest": {"Bills"}, "context": {"bogus"}}, hx), 422, "bogus context")
}

func TestCorrectionWithoutHTMXRedirects(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	path := "/messages/" + s.id(s.ids.other) + "/correct"
	rec := s.post(path, url.Values{"dest": {"Bills"}})
	status(t, rec, 303, "redirect")
	if got := rec.Header().Get("Location"); got != "/messages/"+s.id(s.ids.other) {
		t.Errorf("Location = %q", got)
	}
	failed := status(t, s.post(path, url.Values{"dest": {"a|b"}}), 200, "failed")
	contains(t, failed, "<html", "invalid destination")
}

func TestTriageFullPartialAndPaging(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	full := status(t, s.get("/triage"), 200, "full")
	contains(t, full, "<html", "Weekly news", `id="triage-tabs"`)
	pageSize = 1
	t.Cleanup(func() { pageSize = 50 })
	first := status(t, s.get("/triage", hx), 200, "first")
	contains(t, first, "Show older decisions", `id="decision-list"`)
	lacks(t, first, "<html", "autofocus")
	more := status(t, s.get("/triage?offset=1", hx), 200, "more")
	contains(t, more, "<li", "autofocus")
	lacks(t, more, "decision-list", "Show older decisions")
	status(t, s.get("/triage?stage=operator&agreement=pending&account=icloud", hx), 200, "filtered")
	status(t, s.get("/triage?stage=nonsense"), 422, "bad stage")
	status(t, s.get("/triage?offset=x"), 422, "bad offset")
}

func TestTriageBadFilterRendersTheErrorPageAndToast(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	page := status(t, s.get("/triage?stage=nonsense"), 422, "page")
	contains(t, page, "Filter not understood", "Clear filters", "Unknown stage")
	fragment := status(t, s.get("/triage?agreement=wat", hx), 422, "fragment")
	contains(t, fragment, "Pick the filter again.", `id="toaster"`, "Unknown agreement")
	lacks(t, fragment, "<html")
}

func TestTriageSearchFiltersDecisions(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	hit := status(t, s.get("/triage?q=BILLING@shop", hx), 200, "hit")
	contains(t, hit, "Invoice")
	lacks(t, hit, "Weekly news")
	contains(t, status(t, s.get("/triage?q=list.test", hx), 200, "domain"), "Weekly news")
	contains(t, status(t, s.get("/triage?q=zzz-nothing", hx), 200, "none"), "No decisions match", "Clear a filter to see the rest.")
}

func TestTriageRowShowsDeleteProposal(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	if _, err := s.db.Write.Exec("INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "+
		"VALUES (?, 'icloud', 2.0, 'operator', 'r-del', 'delete', NULL, 1.0, '{}')", s.ids.other); err != nil {
		t.Fatal(err)
	}
	page := status(t, s.get("/triage?q=list.test", hx), 200, "page")
	contains(t, page, `font-medium">Delete</span>`)
	lacks(t, page, "Leave in inbox")
}

func TestTriageFullPageShowsSummariesAndFlags(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	if _, err := s.db.Write.Exec("UPDATE messages SET seen = 0 WHERE id = ?", s.ids.invoice); err != nil {
		t.Fatal(err)
	}
	page := status(t, s.get("/triage?account=icloud"), 200, "page")
	contains(t, page, `aria-label="By stage: Rule 2"`, "Unread flags", "Contact", "9B flags", "Shadow mode")
	lacks(t, page, `action="/live/pause"`)
}

func TestRulesLifecycle(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	status(t, s.get("/rules"), 200, "list")
	form := url.Values{
		"id": {"shop"}, "account": {"icloud"}, "action": {"file"}, "dest": {"Bills"},
		"field": {"from_domain", "dkim_d", "subject"}, "op": {"suffix", "in", "present"},
		"value": {"shop.test, mail.shop.test", "a.test,b.test", ""},
	}
	created := s.post("/rules", form)
	status(t, created, 303, "create")
	location := created.Header().Get("Location")
	if location != "/rules?account=icloud&saved=shop&created=1" {
		t.Fatalf("Location = %q", location)
	}
	landed := status(t, s.get(location), 200, "landed")
	contains(t, landed, "Rule created", `data-strip-params="saved,created"`)
	if n := strings.Count(landed, `id="toaster"`); n != 1 {
		t.Errorf("toaster count = %d", n)
	}
	stored := s.rows("SELECT id, account, action, dest, conditions FROM rules")
	if len(stored) != 1 {
		t.Fatalf("rules = %v", stored)
	}
	var conditions []map[string]any
	if err := json.Unmarshal([]byte(stored[0][4].(string)), &conditions); err != nil {
		t.Fatal(err)
	}
	want := `[{"field":"from_domain","op":"suffix","value":["shop.test","mail.shop.test"]},{"field":"dkim_d","op":"in","value":["a.test","b.test"]},{"field":"subject","op":"present"}]`
	if got, _ := json.Marshal(conditions); string(got) != want {
		t.Errorf("conditions = %s", got)
	}
	contains(t, status(t, s.get("/rules?account=icloud"), 200, "filtered"), "shop")
	contains(t, status(t, s.get("/rules/shop/edit"), 200, "edit"), "mail.shop.test", `value="shop" readonly`)
	form.Set("action", "delete")
	form.Set("dest", "")
	form.Set("account", "")
	form["field"], form["op"], form["value"] = []string{"from_addr"}, []string{"eq"}, []string{"x@shop.test"}
	updated := s.post("/rules/shop", form)
	status(t, updated, 303, "update")
	if got := updated.Header().Get("Location"); got != "/rules?saved=shop" {
		t.Fatalf("Location = %q", got)
	}
	contains(t, status(t, s.get(updated.Header().Get("Location")), 200, "saved"), "Rule saved")
	row := s.rows("SELECT account, action, dest FROM rules")[0]
	if row[0] != nil || row[1] != "delete" || row[2] != nil {
		t.Errorf("rule = %v", row)
	}
}

func TestRuleValidationErrorsRerender(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	bad := status(t, s.post("/rules", url.Values{"id": {"x"}, "action": {"file"}, "dest": {""}, "field": {"from_addr"}, "op": {"eq"}, "value": {"a"}}), 200, "bad")
	contains(t, bad, "The rule was not saved", "Choose a destination")
	if n := s.column("SELECT COUNT(*) FROM rules"); n[0] != "0" {
		t.Errorf("a refused rule was stored")
	}
	ghost := url.Values{"action": {"file"}, "dest": {"A"}, "field": {"from_addr"}, "op": {"eq"}, "value": {"a"}}
	contains(t, status(t, s.post("/rules/ghost", ghost), 200, "ghost update"), "Unknown rule")
	status(t, s.get("/rules/ghost/edit"), 404, "ghost edit")
	empty := status(t, s.post("/rules/preview", url.Values{"action": {"file"}, "dest": {"A"}}), 200, "incomplete")
	contains(t, empty, "Add at least one condition.")
}

func TestRuleNewFromMessagePrefills(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	page := status(t, s.get("/rules/new?message="+s.id(s.ids.invoice)), 200, "prefilled")
	contains(t, page, "billing@shop.test", "Invoice", "Prefilled from", "From the message: add a condition", "data-suggestion")
	status(t, s.get("/rules/new?message=9999"), 404, "unknown message")
	blank := status(t, s.get("/rules/new?account=gmail"), 200, "blank")
	contains(t, blank, "Generated if empty", `href="/rules?account=gmail"`)
	lacks(t, blank, "data-suggestion")
}

func TestRuleConditionRowAndPreview(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	row := status(t, s.get("/rules/condition?field=subject&op=present"), 200, "valueless")
	contains(t, row, "No value needed")
	lacks(t, row, "autofocus")
	status(t, s.get("/rules/condition"), 200, "default")
	added := status(t, s.get("/rules/condition?focus=field"), 200, "added")
	if !matches(`name="field"[^>]*autofocus`, added) {
		t.Errorf("the field select is not focused: %s", added)
	}
	changed := status(t, s.get("/rules/condition?op=present&focus=op"), 200, "changed")
	if !matches(`name="op"[^>]*autofocus`, changed) {
		t.Errorf("the op select is not focused: %s", changed)
	}
	preview := status(t, s.post("/rules/preview", url.Values{
		"account": {"icloud"}, "action": {"file"}, "dest": {"Bills"}, "field": {"from_domain"}, "op": {"eq"}, "value": {"shop.test"},
	}), 200, "preview")
	contains(t, preview, "Invoice")
	broken := status(t, s.post("/rules/preview", url.Values{"action": {"file"}, "dest": {"A"}, "field": {"nope"}, "op": {"eq"}, "value": {"a"}}), 200, "broken")
	contains(t, broken, "Unknown field", "Not a complete rule yet")
}

func orderOf(s *site) []string {
	return s.column("SELECT id FROM rules ORDER BY position")
}

func TestRuleMoveAndDelete(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	for _, name := range []string{"one", "two", "three"} {
		status(t, s.post("/rules", url.Values{"id": {name}, "action": {"file"}, "dest": {"Bills"}, "field": {"from_addr"}, "op": {"eq"}, "value": {name}}), 303, name)
	}
	check := func(want ...string) {
		t.Helper()
		if got := strings.Join(orderOf(s), ","); got != strings.Join(want, ",") {
			t.Fatalf("order = %s, want %v", got, want)
		}
	}
	check("one", "two", "three")
	status(t, s.post("/rules/three/move", url.Values{"direction": {"up"}}, hx), 200, "up")
	check("one", "three", "two")
	s.post("/rules/one/move", url.Values{"direction": {"up"}}, hx)
	check("one", "three", "two")
	s.post("/rules/one/move", url.Values{"direction": {"down"}}, hx)
	check("three", "one", "two")
	status(t, s.post("/rules/ghost/move", url.Values{"direction": {"up"}}), 404, "ghost")
	status(t, s.post("/rules/one/move", url.Values{"direction": {"sideways"}}), 422, "sideways")
	status(t, s.post("/rules/one/move", nil), 422, "missing direction")
	last := status(t, s.post("/rules/two/move", url.Values{"direction": {"down"}}, hx), 200, "last")
	if !matches(`aria-label="Move two up"[^>]*autofocus`, last) {
		t.Errorf("the boundary move does not focus the opposite button: %s", last)
	}
	gone := status(t, s.do("DELETE", "/rules/one", nil, hx), 200, "delete")
	contains(t, gone, "Rule deleted", `href="/rules/two/edit" autofocus`)
	check("three", "two")
	contains(t, status(t, s.do("DELETE", "/rules/one", nil, hx), 200, "delete again"), "Rule not found")
}

func TestRuleIDsWithSpecialCharactersReachTheRule(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	name := "a b&c<d>é?#%"
	status(t, s.post("/rules", url.Values{"id": {name}, "action": {"file"}, "dest": {"Bills"}, "field": {"from_addr"}, "op": {"eq"}, "value": {"x"}}), 303, "create")
	list := status(t, s.get("/rules"), 200, "list")
	escaped := py.URLQuote(name)
	contains(t, list, `href="/rules/`+escaped+`/edit"`)
	lacks(t, list, "%2525")
	status(t, s.get("/rules/"+escaped+"/edit"), 200, "edit")
	status(t, s.post("/rules/"+escaped+"/move", url.Values{"direction": {"up"}}, hx), 200, "move")
	status(t, s.do("DELETE", "/rules/"+escaped+"?account=", nil, hx), 200, "delete")
	if n := s.column("SELECT COUNT(*) FROM rules"); n[0] != "0" {
		t.Errorf("the rule survived its delete: %v", n)
	}
}

func TestRuleEditorSavesAndReopensAnAgeCondition(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	saved := s.post("/rules", url.Values{
		"id": {""}, "account": {"icloud"}, "action": {"delete"}, "dest": {""},
		"field": {"subject", "age_days"}, "op": {"re", "gte"}, "value": {"code", "7"},
	})
	status(t, saved, 303, "save")
	rows := s.rows("SELECT id, conditions FROM rules")
	if len(rows) != 1 || !strings.Contains(rows[0][1].(string), `"value": 7`) {
		t.Fatalf("stored = %v", rows)
	}
	contains(t, status(t, s.get("/rules/"+rows[0][0].(string)+"/edit"), 200, "reopen"), `value="7"`, "Age in days")
}

func TestRulesLearnedTab(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	page := status(t, s.get("/rules"), 200, "page")
	contains(t, page, "Learned", "sender address", "pay@bank.test", "Account")
	scoped := status(t, s.get("/rules?account=gmail"), 200, "scoped")
	lacks(t, scoped, "pay@bank.test")
	contains(t, scoped, "Nothing learned yet.")
}

func TestPauseAndResumeReturnToTheReferer(t *testing.T) {
	s := seeded(t, "live", Options{})
	paused := s.post("/live/pause", nil, [2]string{"Referer", "http://testserver/triage?stage=llm"})
	status(t, paused, 303, "pause")
	if got := paused.Header().Get("Location"); got != "/triage?stage=llm" {
		t.Errorf("Location = %q", got)
	}
	contains(t, status(t, s.get("/triage"), 200, "paused"), "Resume", "Live, paused")
	for _, referer := range []string{"http://testserver//evil.test", "http://elsewhere.test/triage", ""} {
		rec := s.post("/live/resume", nil, [2]string{"Referer", referer})
		status(t, rec, 303, "resume")
		if got := rec.Header().Get("Location"); got != "/triage" {
			t.Errorf("referer %q: Location = %q", referer, got)
		}
	}
	contains(t, status(t, s.get("/triage"), 200, "resumed"), "Pause")
}

func TestMutatingRoutesNeedACSRFToken(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	s.cookie = ""
	for _, route := range [][2]string{
		{"POST", "/messages/1/correct"}, {"POST", "/live/pause"}, {"POST", "/live/resume"}, {"POST", "/live/actions/1/undo"},
		{"POST", "/rules"}, {"POST", "/rules/preview"}, {"POST", "/rules/x"}, {"POST", "/rules/x/move"}, {"DELETE", "/rules/x"},
	} {
		if rec := s.do(route[0], route[1], nil); rec.Code != 403 {
			t.Errorf("%s %s without a token = %d", route[0], route[1], rec.Code)
		}
	}
}

func TestToastsAreOutOfBandFragmentsOnlyWhereThePythonAppHadThem(t *testing.T) {
	s := seeded(t, "shadow", Options{})
	row := status(t, s.post("/messages/"+s.id(s.ids.invoice)+"/correct", url.Values{"dest": {"Bills"}, "context": {"row"}}, hx), 200, "row")
	if n := strings.Count(row, `hx-swap-oob="beforeend"`); n != 1 {
		t.Errorf("oob toasts = %d", n)
	}
	card := status(t, s.post("/messages/"+s.id(s.ids.other)+"/correct", url.Values{"dest": {"Bills"}}, hx), 200, "card")
	lacks(t, card, "hx-swap-oob")
}

func matches(pattern, text string) bool {
	m, _ := rx.MustCompile(pattern, 0).Search(text)
	return m != nil
}
