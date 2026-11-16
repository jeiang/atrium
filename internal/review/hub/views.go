package hub

import (
	"github.com/jeiang/atrium/internal/review"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

type ProposalView struct {
	ID            int64           `json:"id"`
	Kind          string          `json:"kind"`
	KindLabel     string          `json:"kind_label"`
	KindIcon      string          `json:"kind_icon"`
	Status        string          `json:"status"`
	Account       *string         `json:"account"`
	Title         string          `json:"title"`
	CreatedTS     float64         `json:"created_ts"`
	DecidedTS     *float64        `json:"decided_ts"`
	AppliedTS     *float64        `json:"applied_ts"`
	Undoable      bool            `json:"undoable"`
	Blocked       bool            `json:"blocked"`
	BlockedEffect bool            `json:"blocked_effect"`
	BlockedUndo   bool            `json:"blocked_undo"`
	BlockedError  string          `json:"blocked_error"`
	Preview       any             `json:"preview"`
	Result        any             `json:"result"`
	Approval      bool            `json:"approval"`
	Args          []reviewapp.Arg `json:"args"`
}

func proposalView(c reviewapp.Card) ProposalView {
	v := ProposalView{
		ID: c.ID, Kind: c.Kind, KindLabel: c.KindLabel, KindIcon: c.KindIcon, Status: c.Status, Account: c.Account, Title: c.Title,
		CreatedTS: c.CreatedAt, DecidedTS: c.DecidedAt, AppliedTS: c.AppliedAt, Undoable: c.Undoable, Blocked: c.Blocked,
		BlockedEffect: c.BlockedEffect, BlockedUndo: c.BlockedUndo, BlockedError: c.BlockedError,
		Preview: c.Preview, Result: c.Result, Approval: c.Approval,
	}
	if c.Approval {
		v.Args = c.Args
	}
	return v
}

func proposalViews(list []reviewapp.Card) []ProposalView {
	out := make([]ProposalView, len(list))
	for i, c := range list {
		out[i] = proposalView(c)
	}
	return out
}

type proposalCardData struct {
	Proposal   ProposalView `json:"proposal"`
	PendingOOB bool         `json:"pending_oob"`
}

type activityPage struct {
	Entries    []reviewapp.ActivityEntry `json:"entries"`
	Category   string                    `json:"category"`
	Categories []string                  `json:"categories"`
	Next       string                    `json:"next"`
	After      string                    `json:"after"`
}

type activityRowData struct {
	E reviewapp.ActivityEntry `json:"e"`
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

func draftViews(drafts []review.Draft) []DraftView {
	out := make([]DraftView, len(drafts))
	for i, d := range drafts {
		out[i] = DraftViewOf(d)
	}
	return out
}

func DraftViewOf(d review.Draft) DraftView {
	return DraftView{
		ID: d.ID, Status: d.Status, Revision: d.Revision, Account: d.Account, InReplyToMessageID: d.InReplyToMessageID, ThreadID: d.ThreadID,
		UpdatedAt: d.UpdatedAt, Unexpected: d.Unexpected, ToAddrs: d.ToAddrs, CcAddrs: d.CcAddrs, Subject: d.Subject,
		Body: d.Body, SMTPResult: d.SMTPResult,
	}
}
