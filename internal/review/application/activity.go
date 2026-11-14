package application

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
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
	After       *ActivityKey
	Limit       int
	Operations  bool
	HidePrivate bool
}

type Activity struct {
	Entries []ActivityEntry
	Next    *ActivityKey
}

type ActivityKey struct {
	TS     float64 `json:"t"`
	ID     int64   `json:"i"`
	Source string  `json:"s"`
}

var activitySources = []string{SourceOperation, SourceMail, "phone", SourceSettings, "effect"}

func (k ActivityKey) Valid() bool {
	return k.TS >= 0 && !math.IsInf(k.TS, 0) && k.ID > 0 && k.ID < math.MaxInt64 && slices.Contains(activitySources, k.Source)
}

func (k ActivityKey) String() string {
	return strconv.FormatFloat(k.TS, 'g', -1, 64) + "_" + strconv.FormatInt(k.ID, 10) + "_" + k.Source
}

func ParseActivityKey(raw string) (ActivityKey, bool) {
	parts := strings.Split(raw, "_")
	if len(parts) != 3 {
		return ActivityKey{}, false
	}
	ts, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return ActivityKey{}, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ActivityKey{}, false
	}
	k := ActivityKey{TS: ts, ID: id, Source: parts[2]}
	return k, k.Valid()
}

func keyOf(e ActivityEntry) ActivityKey { return ActivityKey{TS: e.TS, ID: e.ID, Source: e.Source} }

func compareKeys(a, b ActivityKey) int {
	if c := cmp.Compare(b.TS, a.TS); c != 0 {
		return c
	}
	if c := cmp.Compare(b.ID, a.ID); c != 0 {
		return c
	}
	return strings.Compare(b.Source, a.Source)
}

func (in ActivityInput) bound(source string) (float64, int64) {
	k := in.After
	if k == nil {
		return 0, 0
	}
	if source < k.Source {
		return k.TS, k.ID + 1
	}
	return k.TS, k.ID
}

func (s *Service) Activity(ctx context.Context, in ActivityInput) (application.Read[Activity], error) {
	if in.Category != "" && !slices.Contains(Categories, in.Category) {
		return application.Read[Activity]{}, invalid("category", "unknown category")
	}
	if in.After != nil && !in.After.Valid() {
		return application.Read[Activity]{}, invalid("after", "unknown position")
	}
	if in.Limit <= 0 {
		in.Limit = ActivityPage
	}
	return read(ctx, s, ActivityName, []string{state.Mail, state.Effects, state.Settings}, in, func() (Activity, error) {
		return s.timeline(ctx, in)
	}, func(a Activity) any {
		if in.Operations {
			return a.Entries
		}
		return mailMarks(a)
	})
}

type privacy struct {
	threads  map[int64]bool
	projects map[int64]bool
}

func (p privacy) hidesTarget(target string) bool {
	kind, raw, ok := strings.Cut(target, ":")
	if !ok {
		return false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return false
	}
	switch kind {
	case "thread":
		return p.threads[id]
	case "project":
		return p.projects[id]
	}
	return false
}

func (s *Service) operationEntries(ctx context.Context, in ActivityInput, hidden privacy, fetch int) (sourcePage, error) {
	ts, id := in.bound(SourceOperation)
	rows, err := s.state.AuditBefore(ctx, ts, id, fetch)
	if err != nil {
		return sourcePage{}, err
	}
	page := sourcePage{}
	for _, r := range rows {
		page.saw(ActivityKey{TS: r.TS, ID: r.ID, Source: SourceOperation}, len(rows), fetch)
		if r.ThreadID != nil && hidden.threads[*r.ThreadID] || hidden.hidesTarget(r.Target) {
			continue
		}
		var linked struct {
			EffectIDs []int64 `json:"effect_ids"`
		}
		_ = json.Unmarshal([]byte(r.DetailsJSON), &linked)
		if linked.EffectIDs == nil {
			linked.EffectIDs = []int64{}
		}
		page.entries = append(page.entries, ActivityEntry{
			Source: SourceOperation, ID: r.ID, Category: CategoryOperations, TS: r.TS,
			Operation: &OperationActivity{
				Name: r.Operation, Actor: r.Actor, Source: r.Source, Outcome: r.Outcome, Target: r.Target,
				ThreadID: r.ThreadID, RunID: r.RunID, CommandID: r.CommandID, EffectIDs: linked.EffectIDs,
			},
		})
	}
	return page, nil
}

type sourcePage struct {
	entries  []ActivityEntry
	frontier *ActivityKey
}

func (p *sourcePage) saw(k ActivityKey, rows, fetch int) {
	if rows == fetch {
		p.frontier = &k
	}
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

func (s *Service) timeline(ctx context.Context, in ActivityInput) (Activity, error) {
	q := s.db.Read
	category := in.Category
	fetch := in.Limit + 1
	var hidden privacy
	if in.HidePrivate {
		var err error
		if hidden.threads, err = projects.PrivateThreadIDs(ctx, q); err != nil {
			return Activity{}, err
		}
		if hidden.projects, err = projects.PrivateIDs(ctx, q); err != nil {
			return Activity{}, err
		}
	}
	var pages []sourcePage
	if in.Operations && category == "" {
		page, err := s.operationEntries(ctx, in, hidden, fetch)
		if err != nil {
			return Activity{}, err
		}
		pages = append(pages, page)
	}
	if category == "" || category == CategoryMail {
		ts, id := in.bound(SourceMail)
		actions, err := s.live.Before(ctx, ts, id, fetch)
		if err != nil {
			return Activity{}, err
		}
		var mail sourcePage
		for _, a := range actions {
			e := MailEntry(a)
			mail.saw(keyOf(e), len(actions), fetch)
			mail.entries = append(mail.entries, e)
		}
		ts, id = in.bound("phone")
		found, err := ledger.ResolutionsBefore(ctx, q, "phone", ts, id, fetch)
		if err != nil {
			return Activity{}, err
		}
		var phone sourcePage
		for _, r := range found {
			e := ActivityEntry{
				ID: r.ID, TS: r.TS, Source: "phone", Category: CategoryMail,
				Phone: &PhoneActivity{Actor: r.Actor, Kind: phoneKind(r), Status: r.Outcome, Target: phoneTarget(r)},
			}
			phone.saw(keyOf(e), len(found), fetch)
			phone.entries = append(phone.entries, e)
		}
		pages = append(pages, mail, phone)
	}
	if category == "" || category == CategorySettings {
		ts, id := in.bound(SourceSettings)
		rows, err := audit.Before(ctx, q, ts, id, fetch)
		if err != nil {
			return Activity{}, err
		}
		var settings sourcePage
		for _, row := range rows {
			e := ActivityEntry{ID: row.ID, TS: row.TS, Source: SourceSettings, Category: CategorySettings}
			e.Settings = &SettingsActivity{Tool: row.Key, Actor: row.Actor, Origin: row.Source, Before: deref(row.Old), After: deref(row.New)}
			settings.saw(keyOf(e), len(rows), fetch)
			settings.entries = append(settings.entries, e)
		}
		pages = append(pages, settings)
	}
	if category != CategoryMail && category != CategorySettings && s.ledger != nil {
		filter := effectFilter(category, fetch)
		filter.ByTime = true
		filter.BeforeTS, filter.BeforeID = in.bound("effect")
		effects, err := s.ledger.List(ctx, filter)
		if err != nil {
			return Activity{}, err
		}
		var writes sourcePage
		for _, effect := range effects {
			writes.saw(ActivityKey{TS: effect.CreatedAt, ID: effect.ID, Source: "effect"}, len(effects), fetch)
			if effect.Direction != "forward" || effect.ThreadID != nil && hidden.threads[*effect.ThreadID] {
				continue
			}
			writes.entries = append(writes.entries, EffectEntry(effect))
		}
		pages = append(pages, writes)
	}
	return merge(pages, in.Limit), nil
}

func merge(pages []sourcePage, limit int) Activity {
	var horizon *ActivityKey
	var entries []ActivityEntry
	for _, p := range pages {
		if p.frontier != nil && (horizon == nil || compareKeys(*p.frontier, *horizon) < 0) {
			horizon = p.frontier
		}
	}
	for _, p := range pages {
		for _, e := range p.entries {
			if horizon == nil || compareKeys(keyOf(e), *horizon) <= 0 {
				entries = append(entries, e)
			}
		}
	}
	slices.SortFunc(entries, func(a, b ActivityEntry) int { return compareKeys(keyOf(a), keyOf(b)) })
	out := Activity{Entries: []ActivityEntry{}, Next: horizon}
	if len(entries) > limit {
		entries = entries[:limit]
		next := keyOf(entries[limit-1])
		out.Next = &next
	}
	out.Entries = append(out.Entries, entries...)
	return out
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
