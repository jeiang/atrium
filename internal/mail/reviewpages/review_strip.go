package reviewpages

import (
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/web"
)

const (
	stripItems  = 3
	recentItems = 6
)

type stripData struct {
	Proposals  []ProposalView  `json:"proposals"`
	Blocked    []ProposalView  `json:"blocked"`
	Drafts     []DraftView     `json:"drafts"`
	Configured map[string]bool `json:"configured"`
	Count      int             `json:"count"`
	More       bool            `json:"more"`
}

type receiptsData struct {
	Entries []ActivityEntry `json:"entries"`
}

func (s *service) needsStrip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	found := s.needs(ctx)
	left := stripItems
	take := func(n int) int {
		n = min(n, left)
		left -= n
		return n
	}
	pending := found.pending[:take(len(found.pending))]
	blocked := found.blocked[:take(len(found.blocked))]
	rows := found.drafts[:take(len(found.drafts))]
	count, err := needsCount(ctx, s.deps.DB.Read)
	check(err)
	s.fragment(w, r, http.StatusOK, "_review_strip.gohtml", stripData{
		Proposals: proposalViews(pending, s.registry), Blocked: proposalViews(blocked, s.registry),
		Drafts: s.draftViews(r, rows), Configured: accountConfigured(ctx),
		Count: count, More: count > stripItems-left,
	}, nil)
}

func (s *service) recentActivity(w http.ResponseWriter, r *http.Request) {
	entries, _, err := timeline(r.Context(), s.deps.DB.Read, s.ledger, "", pageSize, 0, s.now())
	check(err)
	kept := make([]ActivityEntry, 0, recentItems)
	for _, e := range entries {
		if len(kept) == recentItems {
			break
		}
		if e.Write != nil || e.Mail != nil {
			kept = append(kept, e)
		}
	}
	s.fragment(w, r, http.StatusOK, "_receipts.gohtml", receiptsData{Entries: kept}, nil)
}

func receiptView(r *http.Request) bool {
	return web.FormString(web.FormValues(r), "view", "") == "receipt"
}

var receiptVerbs = map[string]string{
	"task_add": "Added a task", "task_update": "Changed a task", "task_done": "Completed a task",
	"kb_write": "Wrote a knowledge record", "kb_retract": "Retracted a knowledge record", "kb_confirm": "Confirmed a knowledge record",
	"kb_purge": "Purged a knowledge record", "kb_export": "Exported knowledge",
	"mail_move": "Moved mail", "mail_folder_create": "Created a mail folder",
	"draft_email_create": "Drafted an email", "draft_email_update": "Changed a draft",
	"calendar_delete":  "Deleted a calendar event",
	"media_add_magnet": "Added a download", "media_channel_add": "Started watching a show", "media_channel_remove": "Stopped watching a show",
	"schedule_create": "Created a schedule", "schedule_update": "Changed a schedule", "schedule_cancel": "Cancelled a schedule",
	"track_package": "Started tracking a package", "ops_finding": "Filed a server finding",
}

var mailVerbs = map[string]string{"file": "Filed", "flag": "Flagged", "delete": "Deleted", "move": "Moved"}

func (e ActivityEntry) Summary() string {
	switch {
	case e.Write != nil:
		if verb, ok := receiptVerbs[e.Write.Tool]; ok {
			return verb
		}
		return strings.ReplaceAll(e.Write.Tool, "_", " ")
	case e.Mail != nil:
		subject := "(no subject)"
		if e.Mail.Subject != nil && *e.Mail.Subject != "" {
			subject = *e.Mail.Subject
		}
		if verb, ok := mailVerbs[e.Mail.Kind]; ok {
			return verb + " " + subject
		}
		return e.Mail.Kind + " " + subject
	}
	return ""
}
