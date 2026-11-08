package reviewpages

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/textx"
)

type ProposalView struct {
	ID          int64    `json:"id"`
	Kind        string   `json:"kind"`
	KindLabel   string   `json:"kind_label"`
	KindIcon    string   `json:"kind_icon"`
	Status      string   `json:"status"`
	Account     *string  `json:"account"`
	Title       string   `json:"title"`
	CreatedTS   float64  `json:"created_ts"`
	DecidedTS   *float64 `json:"decided_ts"`
	AppliedTS   *float64 `json:"applied_ts"`
	Undoable    bool     `json:"undoable"`
	Blocked     bool     `json:"blocked"`
	BlockedUndo bool     `json:"blocked_undo"`
	Preview     any      `json:"preview"`
	Result      any      `json:"result"`
	Approval    bool     `json:"approval"`
	Args        []Arg    `json:"args"`
}

type Arg struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func proposalView(p *proposals.Proposal, kind proposals.Kind) ProposalView {
	v := ProposalView{
		ID: p.ID, Kind: p.Kind, KindLabel: kind.Label, KindIcon: kind.Icon, Status: p.Status, Account: p.Account, Title: p.Title,
		CreatedTS: p.CreatedTS, DecidedTS: p.DecidedTS, AppliedTS: p.AppliedTS, Undoable: p.Undoable, Blocked: p.Blocked,
		BlockedUndo: p.BlockedUndo, Preview: plainJSON(p.Preview), Result: plainJSON(p.Result), Approval: kind.Approval,
	}
	if kind.Approval {
		v.Args = approvalArgs(p.Preview)
	}
	return v
}

func approvalArgs(preview any) []Arg {
	d, _ := preview.(map[string]any)
	args, _ := d["args"].(map[string]any)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]Arg, 0, len(keys))
	for _, k := range keys {
		label := strings.ReplaceAll(k, "_", " ")
		r, size := utf8.DecodeRuneInString(label)
		out = append(out, Arg{Label: strings.ToUpper(string(r)) + label[size:], Value: argText(args[k])})
	}
	return out
}

func argText(v any) string {
	var text string
	switch x := v.(type) {
	case string:
		text = x
	case bool:
		text = "No"
		if x {
			text = "Yes"
		}
	case int64:
		text = fmt.Sprint(x)
	default:
		text = jsonx.Dumps(x)
	}
	if utf8.RuneCountInString(text) > 200 {
		return textx.Clip(text, 199) + "…"
	}
	return text
}

func plainJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = plainJSON(val)
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

func proposalViews(list []*proposals.Proposal, registry *proposals.Registry) []ProposalView {
	out := make([]ProposalView, len(list))
	for i, p := range list {
		kind, _ := registry.Kind(p.Kind)
		out[i] = proposalView(p, kind)
	}
	return out
}

type proposalCardData struct {
	Proposal   ProposalView `json:"proposal"`
	PendingOOB bool         `json:"pending_oob"`
}

type ActivityEntry struct {
	Source   string            `json:"source"`
	ID       int64             `json:"id"`
	Category string            `json:"category"`
	TS       float64           `json:"ts"`
	Mail     *MailActivity     `json:"mail,omitempty"`
	Write    *WriteActivity    `json:"write,omitempty"`
	Settings *SettingsActivity `json:"settings,omitempty"`
	Phone    *PhoneActivity    `json:"phone,omitempty"`
}

type PhoneActivity struct {
	Actor  string `json:"actor"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Target string `json:"target"`
}

type MailActivity struct {
	MessageID   int64   `json:"message_id"`
	Subject     *string `json:"subject"`
	Kind        string  `json:"kind"`
	FromName    *string `json:"from_name"`
	FromAddr    *string `json:"from_addr"`
	Account     string  `json:"account"`
	Src         *string `json:"src"`
	Dest        *string `json:"dest"`
	Status      string  `json:"status"`
	Error       *string `json:"error"`
	Undoable    bool    `json:"undoable"`
	State       string  `json:"state"`
	Blocked     bool    `json:"blocked"`
	UndoBlocked bool    `json:"undo_blocked"`
}

type WriteActivity struct {
	Tool       string   `json:"tool"`
	Target     string   `json:"target"`
	ThreadID   *int64   `json:"thread_id"`
	ThreadKind *string  `json:"thread_kind"`
	UndoneAt   *float64 `json:"undone_at"`
	Before     string   `json:"before"`
	After      string   `json:"after"`
	State      string   `json:"state"`
	Error      *string  `json:"error"`
	Undoable   bool     `json:"undoable"`
}

type SettingsActivity struct {
	Tool     string `json:"tool"`
	ThreadID *int64 `json:"thread_id"`
	Actor    string `json:"actor"`
	Origin   string `json:"origin"`
	Before   string `json:"before"`
	After    string `json:"after"`
}

func mailEntry(a live.Action, now float64) ActivityEntry {
	return ActivityEntry{
		Source: sourceMail, ID: a.ID, Category: categoryMail, TS: a.TS,
		Mail: &MailActivity{
			MessageID: a.MessageID, Subject: a.Subject, Kind: a.Kind, FromName: a.FromName, FromAddr: a.FromAddr,
			Account: a.Account, Src: a.Src, Dest: a.Dest, Status: a.Status, Error: a.Error,
			Undoable: a.Undoable && !live.Blocked(a, now), Blocked: live.Blocked(a, now), UndoBlocked: live.UndoBlocked(a, now),
		},
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

type DraftView struct {
	ID                 int64    `json:"id"`
	Status             string   `json:"status"`
	Revision           int64    `json:"revision"`
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
		ID: row.ID, Status: string(row.Status), Revision: row.Revision, Account: row.Account, InReplyToMessageID: row.InReplyToMessageID, ThreadID: row.ThreadID,
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
