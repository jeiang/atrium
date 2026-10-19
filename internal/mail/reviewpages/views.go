package reviewpages

import (
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

// ProposalView is one proposal as the card template reads it. Preview and Result are the decoded JSON trees
// with every object a plain map, which the template functions read; a wiki plan keeps its counts as an ordered
// py.Dict, the order the card lists them in.
type ProposalView struct {
	ID        int64    `json:"id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Account   *string  `json:"account"`
	Title     string   `json:"title"`
	CreatedTS float64  `json:"created_ts"`
	DecidedTS *float64 `json:"decided_ts"`
	AppliedTS *float64 `json:"applied_ts"`
	Undoable  bool     `json:"undoable"`
	Preview   any      `json:"preview"`
	Result    any      `json:"result"`
}

const kindWikiPlan = "notion_wiki_plan"

func proposalView(p *proposals.Proposal) ProposalView {
	preview := plainJSON(p.Preview)
	if dict, ok := p.Preview.(py.Dict); ok && p.Kind == kindWikiPlan {
		if counts, found := dict.Get("counts"); found {
			if plain, ok := preview.(map[string]any); ok {
				plain["counts"] = counts
			}
		}
	}
	return ProposalView{
		ID: p.ID, Kind: p.Kind, Status: p.Status, Account: p.Account, Title: p.Title, CreatedTS: p.CreatedTS,
		DecidedTS: p.DecidedTS, AppliedTS: p.AppliedTS, Undoable: p.Undoable, Preview: preview, Result: plainJSON(p.Result),
	}
}

// plainJSON turns every py.Dict of a decoded JSON tree into a map[string]any, the object shape the template
// functions read; lists are rebuilt, scalars are kept.
func plainJSON(v any) any {
	switch x := v.(type) {
	case py.Dict:
		out := make(map[string]any, len(x))
		for _, pair := range x {
			out[pair.Key] = plainJSON(pair.Value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plainJSON(e)
		}
		return out
	}
	return v
}

func proposalViews(list []*proposals.Proposal) []ProposalView {
	out := make([]ProposalView, len(list))
	for i, p := range list {
		out[i] = proposalView(p)
	}
	return out
}

type proposalListData struct {
	Proposals []ProposalView `json:"proposals"`
	Status    string         `json:"status"`
	Kind      string         `json:"kind"`
	Counts    py.Dict        `json:"counts"`
	Partial   bool           `json:"partial"`
}

type proposalCardData struct {
	Proposal   ProposalView `json:"proposal"`
	PendingOOB bool         `json:"pending_oob"`
}

// ActivityEntry is one row of the activity timeline. The mail and write fields are one union so that every key
// a row template reads exists whichever source the row has.
type ActivityEntry struct {
	Source   string  `json:"source"`
	ID       int64   `json:"id"`
	Category string  `json:"category"`
	TS       float64 `json:"ts"`

	MessageID int64   `json:"message_id"`
	Subject   *string `json:"subject"`
	Kind      string  `json:"kind"`
	FromName  *string `json:"from_name"`
	FromAddr  *string `json:"from_addr"`
	Account   string  `json:"account"`
	Src       *string `json:"src"`
	Dest      *string `json:"dest"`
	Status    string  `json:"status"`
	Error     *string `json:"error"`
	Undoable  bool    `json:"undoable"`

	Tool       string   `json:"tool"`
	Target     string   `json:"target"`
	ThreadID   *int64   `json:"thread_id"`
	ThreadKind *string  `json:"thread_kind"`
	UndoneAt   *float64 `json:"undone_at"`
	Before     string   `json:"before"`
	After      string   `json:"after"`
}

func mailEntry(a live.Action) ActivityEntry {
	return ActivityEntry{
		Source: sourceMail, ID: a.ID, Category: categoryMail, TS: a.TS, MessageID: a.MessageID, Subject: a.Subject,
		Kind: a.Kind, FromName: a.FromName, FromAddr: a.FromAddr, Account: a.Account, Src: a.Src, Dest: a.Dest,
		Status: a.Status, Error: a.Error, Undoable: a.Undoable,
	}
}

type activityPage struct {
	Entries    []ActivityEntry `json:"entries"`
	Category   string          `json:"category"`
	Categories []string        `json:"categories"`
	NextOffset int             `json:"next_offset"`
	Offset     int             `json:"offset"`
}

type activityRowData struct {
	E ActivityEntry `json:"e"`
}

// DraftView is a draft with the recipients that were not on the message it answers.
type DraftView struct {
	ID                 int64    `json:"id"`
	Status             string   `json:"status"`
	Account            string   `json:"account"`
	InReplyToMessageID *int64   `json:"in_reply_to_message_id"`
	ThreadID           *int64   `json:"thread_id"`
	UpdatedAt          float64  `json:"updated_at"`
	Unexpected         []string `json:"unexpected"`
	ToAddrs            string   `json:"to_addrs"`
	CcAddrs            string   `json:"cc_addrs"`
	Subject            string   `json:"subject"`
	Body               string   `json:"body"`
	SMTPResult         *string  `json:"smtp_result"`
}

func draftView(row drafts.Row, unexpected []string) DraftView {
	return DraftView{
		ID: row.ID, Status: row.Status, Account: row.Account, InReplyToMessageID: row.InReplyToMessageID, ThreadID: row.ThreadID,
		UpdatedAt: row.UpdatedAt, Unexpected: unexpected, ToAddrs: row.ToAddrs, CcAddrs: row.CcAddrs, Subject: row.Subject,
		Body: row.Body, SMTPResult: row.SMTPResult,
	}
}

type draftsPage struct {
	Drafts     []DraftView     `json:"drafts"`
	Status     string          `json:"status"`
	Statuses   []string        `json:"statuses"`
	Configured map[string]bool `json:"configured"`
}

type draftCardData struct {
	D          DraftView       `json:"d"`
	Configured map[string]bool `json:"configured"`
	Partial    bool            `json:"partial"`
}
