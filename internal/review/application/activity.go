package application

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	SourceMail         = "mail"
	SourceWrite        = "write"
	SourceSettings     = "settings"
	SourceOperation    = "operation"
	CategoryMail       = "mail"
	CategoryAssistant  = "assistant"
	CategoryWiki       = "wiki"
	CategoryKnowledge  = "knowledge"
	CategoryMedia      = "media"
	CategorySettings   = "settings"
	CategoryOperations = "operations"
	ActivityPage       = 50
	snippetLimit       = 800
)

var Categories = []string{CategoryMail, CategoryAssistant, CategoryWiki, CategoryKnowledge, CategoryMedia, CategorySettings}

type ActivityEntry struct {
	Source    string             `json:"source"`
	ID        int64              `json:"id"`
	Category  string             `json:"category"`
	TS        float64            `json:"ts"`
	Mail      *MailActivity      `json:"mail,omitempty"`
	Write     *WriteActivity     `json:"write,omitempty"`
	Settings  *SettingsActivity  `json:"settings,omitempty"`
	Phone     *PhoneActivity     `json:"phone,omitempty"`
	Operation *OperationActivity `json:"operation,omitempty"`
}

type OperationActivity struct {
	Name      string  `json:"name"`
	Actor     string  `json:"actor"`
	Source    string  `json:"source"`
	Outcome   string  `json:"outcome"`
	Target    string  `json:"target"`
	ThreadID  *int64  `json:"thread_id"`
	RunID     *int64  `json:"run_id"`
	CommandID *string `json:"command_id"`
	EffectIDs []int64 `json:"effect_ids"`
}

type PhoneActivity struct {
	Actor  string `json:"actor"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Target string `json:"target"`
}

type MailActivity struct {
	MessageID        int64   `json:"message_id"`
	Subject          *string `json:"subject"`
	Kind             string  `json:"kind"`
	FromName         *string `json:"from_name"`
	FromAddr         *string `json:"from_addr"`
	Account          string  `json:"account"`
	Src              *string `json:"src"`
	Dest             *string `json:"dest"`
	Status           string  `json:"status"`
	Error            *string `json:"error"`
	Undoable         bool    `json:"undoable"`
	State            string  `json:"state"`
	Blocked          bool    `json:"blocked"`
	UndoBlocked      bool    `json:"undo_blocked"`
	Undone           bool    `json:"undone"`
	Resolved         bool    `json:"resolved"`
	EffectiveOutcome string  `json:"effective_outcome,omitempty"`
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
	Resolved   bool     `json:"resolved"`
	Effective  string   `json:"effective_outcome,omitempty"`
}

type SettingsActivity struct {
	Tool     string `json:"tool"`
	ThreadID *int64 `json:"thread_id"`
	Actor    string `json:"actor"`
	Origin   string `json:"origin"`
	Before   string `json:"before"`
	After    string `json:"after"`
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

type ActivityInput struct {
	Category    string
	Offset      int
	Limit       int
	Operations  bool
	HidePrivate bool
}

type Activity struct {
	Entries    []ActivityEntry
	NextOffset int
}

func (s *Service) Activity(ctx context.Context, in ActivityInput) (application.Read[Activity], error) {
	if in.Category != "" && !slices.Contains(Categories, in.Category) {
		return application.Read[Activity]{}, invalid("category", "unknown category")
	}
	in.Offset = max(in.Offset, 0)
	if in.Limit <= 0 {
		in.Limit = ActivityPage
	}
	return read(ctx, s, ActivityName, []string{state.Mail, state.Effects, state.Settings}, in, func() (Activity, error) {
		entries, next, err := s.timeline(ctx, in, in.Limit, in.Offset)
		return Activity{Entries: entries, NextOffset: next}, err
	}, func(a Activity) any {
		if in.Operations {
			return a.Entries
		}
		return mailMarks(a)
	})
}

func (s *Service) operationEntries(ctx context.Context, hidePrivate bool, limit int) ([]ActivityEntry, error) {
	query := "SELECT id, ts, operation, actor, source, thread_id, run_id, outcome, target, details_json, command_id FROM operation_audit"
	if hidePrivate {
		query += " WHERE thread_id IS NULL OR thread_id NOT IN (" + privateThreadsSQL + ")"
	}
	rows, err := s.db.Read.QueryContext(ctx, query+" ORDER BY ts DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ActivityEntry
	for rows.Next() {
		var (
			e       ActivityEntry
			op      OperationActivity
			details string
		)
		if err := rows.Scan(&e.ID, &e.TS, &op.Name, &op.Actor, &op.Source, &op.ThreadID, &op.RunID, &op.Outcome, &op.Target, &details, &op.CommandID); err != nil {
			return nil, err
		}
		var linked struct {
			EffectIDs []int64 `json:"effect_ids"`
		}
		_ = json.Unmarshal([]byte(details), &linked)
		op.EffectIDs = linked.EffectIDs
		if op.EffectIDs == nil {
			op.EffectIDs = []int64{}
		}
		e.Source, e.Category, e.Operation = SourceOperation, CategoryOperations, &op
		out = append(out, e)
	}
	return out, rows.Err()
}

var privateThreadsSQL = "SELECT id FROM threads WHERE project_id IN (" + projects.PrivateIDsSQL() + ")"

func (s *Service) privateThreads(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.Read.QueryContext(ctx, privateThreadsSQL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

var writeCategoryPrefixes = []struct{ Prefix, Category string }{
	{"notion", CategoryWiki},
	{"kb", CategoryKnowledge},
	{"media", CategoryMedia},
}

func writeCategory(tool string) string {
	for _, prefix := range writeCategoryPrefixes {
		if strings.HasPrefix(tool, prefix.Prefix) {
			return prefix.Category
		}
	}
	return CategoryAssistant
}

func effectFilter(category string, limit int) ledger.Filter {
	f := ledger.Filter{Origins: DirectOrigins, Limit: limit}
	switch category {
	case CategoryWiki:
		f.OperationLike = []string{"notion%"}
	case CategoryKnowledge:
		f.OperationLike = []string{"kb%"}
	case CategoryMedia:
		f.OperationLike = []string{"media%"}
	case CategoryAssistant:
		f.OperationNotLike = []string{"notion%", "kb%", "media%"}
	}
	return f
}

func Snippet(raw *string) (string, error) {
	if raw == nil || *raw == "" {
		return "", nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(value, "", " ")
	text := []rune(string(b))
	if len(text) <= snippetLimit {
		return string(text), nil
	}
	return string(text[:snippetLimit]) + "…", nil
}

func RawText(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	text := string(raw)
	return &text
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *Service) timeline(ctx context.Context, in ActivityInput, limit, offset int) ([]ActivityEntry, int, error) {
	q := s.db.Read
	category := in.Category
	want := offset + limit + 1
	var entries []ActivityEntry
	if in.Operations && category == "" {
		found, err := s.operationEntries(ctx, in.HidePrivate, want)
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, found...)
	}
	var private map[int64]bool
	if in.HidePrivate {
		var err error
		if private, err = s.privateThreads(ctx); err != nil {
			return nil, 0, err
		}
	}
	if category == "" || category == CategoryMail {
		actions, err := s.live.List(ctx, "", "", want, 0)
		if err != nil {
			return nil, 0, err
		}
		for _, a := range actions {
			entries = append(entries, MailEntry(a))
		}
		found, err := ledger.ResolutionsSince(ctx, q, "phone", want)
		if err != nil {
			return nil, 0, err
		}
		for _, r := range found {
			entries = append(entries, ActivityEntry{
				ID: r.ID, TS: r.TS, Source: "phone", Category: CategoryMail,
				Phone: &PhoneActivity{Actor: r.Actor, Kind: phoneKind(r), Status: r.Outcome, Target: phoneTarget(r)},
			})
		}
	}
	if category == "" || category == CategorySettings {
		rows, err := audit.Recent(ctx, q, want)
		if err != nil {
			return nil, 0, err
		}
		for _, row := range rows {
			e := ActivityEntry{ID: row.ID, TS: row.TS, Source: SourceSettings, Category: CategorySettings}
			e.Settings = &SettingsActivity{Tool: row.Key, Actor: row.Actor, Origin: row.Source, Before: deref(row.Old), After: deref(row.New)}
			entries = append(entries, e)
		}
	}
	if category != CategoryMail && category != CategorySettings && s.ledger != nil {
		effects, err := s.ledger.List(ctx, effectFilter(category, want))
		if err != nil {
			return nil, 0, err
		}
		for _, effect := range effects {
			if effect.Direction != "forward" || effect.ThreadID != nil && private[*effect.ThreadID] {
				continue
			}
			entries = append(entries, EffectEntry(effect))
		}
	}
	slices.SortStableFunc(entries, func(a, b ActivityEntry) int {
		switch {
		case a.TS != b.TS:
			if a.TS > b.TS {
				return -1
			}
			return 1
		case a.ID > b.ID:
			return -1
		case a.ID < b.ID:
			return 1
		}
		return 0
	})
	next := 0
	if len(entries) > offset+limit {
		next = offset + limit
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	end := min(offset+limit, len(entries))
	shown := entries[offset:end]
	if shown == nil {
		shown = []ActivityEntry{}
	}
	return shown, next, nil
}

func phoneKind(r ledger.ResolutionRow) string {
	if r.EffectID == nil {
		return "proposal"
	}
	return r.Operation
}

func phoneTarget(r ledger.ResolutionRow) string {
	switch {
	case r.EffectID == nil && r.ProposalID != nil:
		return fmt.Sprintf("proposal %d", *r.ProposalID)
	case r.EffectID != nil && r.Origin == ledger.AutoOrigin:
		return fmt.Sprintf("mail %d", *r.EffectID)
	case r.EffectID != nil:
		return fmt.Sprintf("%s %d", r.Operation, *r.EffectID)
	}
	return ""
}

func EffectEntry(e ledger.Effect) ActivityEntry {
	before, _ := Snippet(RawText(e.Before))
	after, _ := Snippet(RawText(e.After))
	var errorText *string
	if e.Error != "" {
		errorText = &e.Error
	}
	return ActivityEntry{
		Source: "effect", ID: e.ID, Category: writeCategory(e.Operation), TS: e.CreatedAt,
		Write: &WriteActivity{
			Tool: e.Operation, Target: e.Target, ThreadID: e.ThreadID, UndoneAt: e.UndoneAt,
			Before: before, After: after, State: string(e.State), Error: errorText, Undoable: e.Undoable,
			Resolved: e.Resolution != nil, Effective: cmp.Or(e.EffectiveOutcome, string(e.State)),
		},
	}
}

func MailEntry(a live.Action) ActivityEntry {
	return ActivityEntry{
		Source: SourceMail, ID: a.ID, Category: CategoryMail, TS: a.TS,
		Mail: &MailActivity{
			MessageID: a.MessageID, Subject: a.Subject, Kind: a.Kind, FromName: a.FromName, FromAddr: a.FromAddr,
			Account: a.Account, Src: a.Src, Dest: a.Dest, Status: a.Status, Error: a.Error,
			Undoable: a.Undoable && !a.Blocked && !a.UndoBlocked, Blocked: a.Blocked || a.UndoBlocked, UndoBlocked: a.UndoBlocked,
			Undone: a.Undone, Resolved: a.Resolved, EffectiveOutcome: a.EffectiveOutcome,
		},
	}
}

type mailMark struct {
	ID          int64 `json:"id"`
	UndoBlocked bool  `json:"undo_blocked"`
}

func mailMarks(a Activity) any {
	var out []mailMark
	for _, e := range a.Entries {
		if e.Mail != nil {
			out = append(out, mailMark{ID: e.ID, UndoBlocked: e.Mail.UndoBlocked})
		}
	}
	return out
}
