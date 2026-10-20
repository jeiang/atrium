package reviewpages

import (
	"bytes"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func goldenEngine(t testing.TB, c tpltest.Case) *tpl.Engine {
	t.Helper()
	globals := map[string]any{}
	for k, v := range c.Globals {
		globals[k] = v
	}
	version, _ := c.Globals["asset_version"].(string)
	e, err := tpl.New(tpl.Config{AssetVersion: version, Globals: globals, Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func goldenFrame(c tpltest.Case, page string, data any) *tpl.Frame {
	f := &tpl.Frame{Data: data, Page: page}
	f.CSRF, _ = c.Globals["csrf_token"].(string)
	f.Mode, _ = c.Globals["mode"].(string)
	if v, ok := c.Context["mode"].(string); ok {
		f.Mode = v
	}
	f.Paused, _ = c.Context["paused"].(bool)
	if n, ok := c.Context["pending_count"].(int64); ok {
		f.PendingCount = int(n)
	}
	if m, ok := c.Context["toast"].(map[string]any); ok {
		f.Toast = &tpl.Toast{Category: m["category"].(string), Title: m["title"].(string), Description: m["description"].(string)}
	}
	return f
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func optStr(m map[string]any, key string) *string {
	if s, ok := m[key].(string); ok {
		return &s
	}
	return nil
}

func optFloat(m map[string]any, key string) *float64 {
	switch n := m[key].(type) {
	case float64:
		return &n
	case int64:
		f := float64(n)
		return &f
	}
	return nil
}

func optInt(m map[string]any, key string) *int64 {
	if n, ok := m[key].(int64); ok {
		return &n
	}
	return nil
}

func num(m map[string]any, key string) float64 {
	if f := optFloat(m, key); f != nil {
		return *f
	}
	return 0
}

func inum(m map[string]any, key string) int64 {
	if n := optInt(m, key); n != nil {
		return *n
	}
	return 0
}

func decoded(t testing.TB, v any) any {
	t.Helper()
	if v == nil {
		return nil
	}
	out, err := py.JSONLoads([]byte(py.JSONDumps(v)))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func goldenProposal(t testing.TB, c tpltest.Case, m map[string]any, path string) ProposalView {
	t.Helper()
	preview := m["preview"]
	if str(m, "kind") == kindWikiPlan {
		if pm, ok := preview.(map[string]any); ok {
			if _, ok := pm["counts"].(map[string]any); ok {
				pm["counts"] = c.Ordered(t, path+".preview.counts")
			}
		}
	}
	p := &proposals.Proposal{
		ID: inum(m, "id"), Kind: str(m, "kind"), Account: optStr(m, "account"), Status: str(m, "status"), CreatedTS: num(m, "created_ts"),
		DecidedTS: optFloat(m, "decided_ts"), AppliedTS: optFloat(m, "applied_ts"), Title: str(m, "title"),
		Preview: decoded(t, preview), Result: decoded(t, m["result"]), Undoable: m["undoable"] == true,
	}
	return proposalView(p)
}

func orderedIndex(i int) string { return strconv.Itoa(i) }

func goldenProposalList(t testing.TB, c tpltest.Case) []ProposalView {
	t.Helper()
	list, _ := c.Context["proposals"].([]any)
	out := make([]ProposalView, len(list))
	for i, e := range list {
		out[i] = goldenProposal(t, c, e.(map[string]any), "proposals."+orderedIndex(i))
	}
	return out
}

func goldenCounts(t testing.TB, c tpltest.Case) py.Dict {
	t.Helper()
	if _, ok := c.Context["counts"].(map[string]any); !ok {
		return nil
	}
	return c.Ordered(t, "counts")
}

func goldenEntry(m map[string]any) ActivityEntry {
	return ActivityEntry{
		Source: str(m, "source"), ID: inum(m, "id"), Category: str(m, "category"), TS: num(m, "ts"),
		MessageID: inum(m, "message_id"), Subject: optStr(m, "subject"), Kind: str(m, "kind"), FromName: optStr(m, "from_name"),
		FromAddr: optStr(m, "from_addr"), Account: str(m, "account"), Src: optStr(m, "src"), Dest: optStr(m, "dest"),
		Status: str(m, "status"), Error: optStr(m, "error"), Undoable: m["undoable"] == true,
		Tool: str(m, "tool"), Target: str(m, "target"), ThreadID: optInt(m, "thread_id"), ThreadKind: optStr(m, "thread_kind"),
		UndoneAt: optFloat(m, "undone_at"), Before: str(m, "before"), After: str(m, "after"),
	}
}

func goldenEntries(c tpltest.Case) []ActivityEntry {
	list, _ := c.Context["entries"].([]any)
	out := make([]ActivityEntry, len(list))
	for i, e := range list {
		out[i] = goldenEntry(e.(map[string]any))
	}
	return out
}

func goldenStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, e := range list {
		out[i], _ = e.(string)
	}
	return out
}

func goldenDraft(m map[string]any) DraftView {
	return draftView(drafts.Row{
		ID: inum(m, "id"), Status: str(m, "status"), Account: str(m, "account"), InReplyToMessageID: optInt(m, "in_reply_to_message_id"),
		ThreadID: optInt(m, "thread_id"), UpdatedAt: num(m, "updated_at"), ToAddrs: str(m, "to_addrs"), CcAddrs: str(m, "cc_addrs"),
		Subject: str(m, "subject"), Body: str(m, "body"), SMTPResult: optStr(m, "smtp_result"),
	}, goldenStrings(m["unexpected"]))
}

func goldenConfigured(c tpltest.Case) map[string]bool {
	out := map[string]bool{}
	m, _ := c.Context["configured"].(map[string]any)
	for k, v := range m {
		out[k], _ = v.(bool)
	}
	return out
}

type fragmentCase struct {
	name string
	data func(testing.TB, tpltest.Case) any
}

func proposalCard(t testing.TB, c tpltest.Case) any {
	m, _ := c.Context["proposal"].(map[string]any)
	oob, _ := c.Context["pending_oob"].(bool)
	return proposalCardData{Proposal: goldenProposal(t, c, m, "proposal"), PendingOOB: oob}
}

func proposalListData_(t testing.TB, c tpltest.Case) any {
	partial, _ := c.Context["partial"].(bool)
	return proposalListData{Proposals: goldenProposalList(t, c), Status: str(c.Context, "status"), Kind: str(c.Context, "kind"), Counts: goldenCounts(t, c), Partial: partial}
}

func activityData(_ testing.TB, c tpltest.Case) any {
	return activityPage{
		Entries: goldenEntries(c), Category: str(c.Context, "category"), Categories: goldenStrings(c.Context["categories"]),
		NextOffset: int(inum(c.Context, "next_offset")), Offset: int(inum(c.Context, "offset")),
	}
}

func activityRow(_ testing.TB, c tpltest.Case) any {
	m, _ := c.Context["e"].(map[string]any)
	return activityRowData{E: goldenEntry(m)}
}

func draftsData(_ testing.TB, c tpltest.Case) any {
	list, _ := c.Context["drafts"].([]any)
	out := make([]DraftView, len(list))
	for i, e := range list {
		out[i] = goldenDraft(e.(map[string]any))
	}
	return draftsPage{Drafts: out, Status: str(c.Context, "status"), Statuses: goldenStrings(c.Context["statuses"]), Configured: goldenConfigured(c)}
}

func draftCard(_ testing.TB, c tpltest.Case) any {
	m, _ := c.Context["d"].(map[string]any)
	partial, _ := c.Context["partial"].(bool)
	return draftCardData{D: goldenDraft(m), Configured: goldenConfigured(c), Partial: partial}
}

var pageGoldens = map[string]struct {
	page string
	data func(testing.TB, tpltest.Case) any
}{
	"proposals":        {"proposals", proposalListData_},
	"proposals@shadow": {"proposals", proposalListData_},
	"activity":         {"activity", activityData},
	"activity@empty":   {"activity", activityData},
	"drafts":           {"drafts", draftsData},
	"drafts@empty":     {"drafts", draftsData},
}

func TestGoldenPages(t *testing.T) {
	for name, spec := range pageGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c, spec.page, spec.data(t, c))); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), pageRewrites[name]...)
		})
	}
}

var fragmentGoldens = map[string]func(testing.TB, tpltest.Case) any{
	"_proposal_list": proposalListData_, "_proposal_list@one": proposalListData_, "_proposal_list@empty": proposalListData_,
	"_proposal_list@empty-pending": proposalListData_,
	"_proposal":                    proposalCard,
	"_activity_rows":               activityData, "_activity_rows@empty": activityData,
	"_activity_row": activityRow, "_activity_row@mail": activityRow,
	"_draft": draftCard, "_draft@sent": draftCard, "_draft@unknown": draftCard, "_draft_preview": draftCard,
}

func TestGoldenFragments(t *testing.T) {
	names := map[string]func(testing.TB, tpltest.Case) any{}
	for name, data := range fragmentGoldens {
		names[name] = data
	}
	for _, name := range proposalCardGoldens {
		names[name] = proposalCard
	}
	for name, data := range names {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Fragment(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c, "", data(t, c))); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), fragmentRewrites[name]...)
		})
	}
}

var proposalCardGoldens = []string{
	"_proposal@01-rebalance_split-pending", "_proposal@02-inbox_backlog-pending", "_proposal@03-rebalance_merge-pending",
	"_proposal@05-gmail_label_merge-pending", "_proposal@06-notion_link-pending", "_proposal@07-calendar_event-pending",
	"_proposal@08-reminder-pending", "_proposal@09-notion_edit-pending", "_proposal@10-notion_wiki_plan-pending",
	"_proposal@11-rebalance_split-applied", "_proposal@12-notion_wiki_plan-failed", "_proposal@13-deletion_rule-dismissed",
	"_proposal@14-reminder-suppressed", "_proposal@15-rebalance_split-approved", "_proposal@16-gmail_label_merge-applied",
	"_proposal@shadow-deletion",
}

var pageRewrites = map[string][]tpltest.Rewrite{}

var fragmentRewrites = map[string][]tpltest.Rewrite{}

func goldenMaps(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i], _ = e.(map[string]any)
	}
	return out
}

func goldenLoops(v any) []openloops.Loop {
	var out []openloops.Loop
	for _, m := range goldenMaps(v) {
		out = append(out, openloops.Loop{
			ID: inum(m, "id"), Account: str(m, "account"), Days: inum(m, "days"), Counterpart: str(m, "counterpart"),
			LastMessageID: inum(m, "last_message_id"), Subject: str(m, "subject"),
		})
	}
	return out
}

func loopsData(_ testing.TB, c tpltest.Case) any {
	var commitments []openloops.Commitment
	for _, m := range goldenMaps(c.Context["commitments"]) {
		commitments = append(commitments, openloops.Commitment{
			ID: inum(m, "id"), MessageID: inum(m, "message_id"), Text: str(m, "text"), Owner: str(m, "owner"), Due: optStr(m, "due"),
			Subject: optStr(m, "subject"),
		})
	}
	var choices []int
	for _, n := range c.Context["snooze_choices"].([]any) {
		choices = append(choices, int(n.(int64)))
	}
	return loopsPage{Owe: goldenLoops(c.Context["owe"]), Waiting: goldenLoops(c.Context["waiting"]), Commitments: commitments, SnoozeChoices: choices}
}

func watchesData(_ testing.TB, c tpltest.Case) any {
	var packages []watches.Package
	for _, m := range goldenMaps(c.Context["packages"]) {
		packages = append(packages, watches.Package{
			ID: inum(m, "id"), Merchant: str(m, "merchant"), Carrier: str(m, "carrier"), Status: str(m, "status"),
			TrackingNumber: str(m, "tracking_number"), OrderRef: str(m, "order_ref"), StatusText: str(m, "status_text"),
			Updated: str(m, "updated"), LastMessageID: inum(m, "last_message_id"),
		})
	}
	page := watchesPage{Packages: packages}
	if c.Context["birthdays"] != nil {
		items := []birthdays.Item{}
		for _, m := range goldenMaps(c.Context["birthdays"]) {
			item := birthdays.Item{Name: str(m, "name"), Label: str(m, "label"), Date: str(m, "date"), DaysUntil: int(inum(m, "days_until")), Key: str(m, "key")}
			if n := optInt(m, "turning"); n != nil {
				turning := int(*n)
				item.Turning = &turning
			}
			items = append(items, item)
		}
		page.Birthdays = &items
	}
	return page
}

func init() {
	pageGoldens["openloops"] = struct {
		page string
		data func(testing.TB, tpltest.Case) any
	}{"loops", loopsData}
	pageGoldens["openloops@empty"] = pageGoldens["openloops"]
	pageGoldens["watches"] = struct {
		page string
		data func(testing.TB, tpltest.Case) any
	}{"watches", watchesData}
	pageGoldens["watches@empty"] = pageGoldens["watches"]
	pageGoldens["watches@no-contacts"] = pageGoldens["watches"]
}
