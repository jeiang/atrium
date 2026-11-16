package application

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/signals"
	"github.com/jeiang/atrium/internal/tasks"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
)

const (
	ListName    application.Name = "commitments.list"
	GetName     application.Name = "commitments.get"
	DoneName    application.Name = "commitments.done"
	ReopenName  application.Name = "commitments.reopen"
	DismissName application.Name = "commitments.dismiss"
	UpdateName  application.Name = "commitments.update"
	DeleteName  application.Name = "commitments.delete"
)

const (
	SideTodo    = "todo"
	SideWaiting = "waiting"

	SourceTask       = "task"
	SourceCommitment = "commitment"

	OwnerMe   = "me"
	OwnerThem = "them"

	ActionDone    = "done"
	ActionReopen  = "reopen"
	ActionDismiss = "dismiss"
	ActionEdit    = "edit"
	ActionDelete  = "delete"

	ReasonReadOnly = "read_only"

	MaxLimit = 200
)

var Sides = [...]string{SideTodo, SideWaiting}

var Statuses = [...]string{tasks.StatusOpen, tasks.StatusDone, tasks.StatusDismissed}

var ErrNoModuleSet = errors.New("commitments: no module set")

type CommitmentItem struct {
	Ref         string       `json:"ref"`
	Source      string       `json:"source" api:"enum=task|commitment"`
	ID          int64        `json:"id"`
	Side        string       `json:"side" api:"enum=todo|waiting"`
	Title       string       `json:"title"`
	Notes       string       `json:"notes"`
	Due         *string      `json:"due" api:"nullable"`
	Status      string       `json:"status" api:"enum=open|done|dismissed"`
	Owner       string       `json:"owner" api:"enum=me|them"`
	Counterpart *string      `json:"counterpart" api:"nullable"`
	Priority    *string      `json:"priority" api:"nullable"`
	RemindAt    *float64     `json:"remind_at" api:"nullable"`
	CreatedTS   float64      `json:"created_ts"`
	UpdatedTS   *float64     `json:"updated_ts" api:"nullable"`
	DoneTS      *float64     `json:"done_ts" api:"nullable"`
	Links       []tasks.Link `json:"links"`
	Actions     []string     `json:"actions"`
}

type Key struct {
	Side      int     `json:"s"`
	Rank      int     `json:"r"`
	NoDue     int     `json:"n"`
	Due       string  `json:"d"`
	CreatedTS float64 `json:"c"`
	Source    string  `json:"k"`
	ID        int64   `json:"id"`
}

func (k Key) Valid() bool {
	return k.Side >= 0 && k.Side <= 1 && k.Rank >= 0 && k.Rank <= 2 && k.NoDue >= 0 && k.NoDue <= 1 &&
		(k.Source == SourceTask || k.Source == SourceCommitment) && k.ID > 0
}

func (k Key) compare(o Key) int {
	return cmp.Or(cmp.Compare(k.Side, o.Side), cmp.Compare(k.Rank, o.Rank), cmp.Compare(k.NoDue, o.NoDue),
		strings.Compare(k.Due, o.Due), cmp.Compare(k.CreatedTS, o.CreatedTS), strings.Compare(k.Source, o.Source), cmp.Compare(k.ID, o.ID))
}

func (item CommitmentItem) Key() Key {
	k := Key{NoDue: 1, CreatedTS: item.CreatedTS, Source: item.Source, ID: item.ID}
	if item.Side == SideWaiting {
		k.Side = 1
	}
	switch item.Status {
	case tasks.StatusOpen:
	case tasks.StatusDone:
		k.Rank = 1
	default:
		k.Rank = 2
	}
	if item.Due != nil && *item.Due != "" {
		k.NoDue, k.Due = 0, *item.Due
	}
	return k
}

type CommitmentStatusCounts struct {
	Open      int `json:"open"`
	Done      int `json:"done"`
	Dismissed int `json:"dismissed"`
}

type CommitmentCounts struct {
	Todo    CommitmentStatusCounts `json:"todo"`
	Waiting CommitmentStatusCounts `json:"waiting"`
}

type ListInput struct {
	Side      string
	Status    string
	Filter    tasks.Filter
	After     *Key
	Limit     int
	TasksOnly bool
}

type List struct {
	Items  []CommitmentItem
	Next   *Key
	Counts CommitmentCounts
	Marks  tool.Marks
}

type RefInput struct {
	Source tasksapp.Source
	Ref    string
}

type UpdateInput struct {
	Source    tasksapp.Source
	Ref       string
	Title     *string
	Notes     *string
	Due       *string
	RemindAt  *float64
	Priority  *string
	Link      *tasksapp.LinkInput
	Clear     []string
	UpdatedTS *float64
}

type Changed struct {
	Ref    string
	Status string
	Item   *CommitmentItem
}

type Service struct {
	db     *db.DB
	state  *state.Store
	ledger *ledger.Store
	tasks  *tasksapp.Service
}

func New(d *db.DB, store *ledger.Store, now func() float64) *Service {
	return &Service{db: d, state: state.New(d), ledger: store, tasks: tasksapp.New(d, store, now)}
}

func describeRead[I, O any](name application.Name, authority ...application.Authority) application.Definition {
	return application.Describe[I, O](name, application.Query, authority, application.NoEffect, false)
}

func describeCommand[I any](name application.Name) application.Definition {
	return application.Describe[I, Changed](name, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, true)
}

func Describe() []application.Definition {
	return []application.Definition{
		describeRead[ListInput, List](ListName, application.Agent, application.Operator),
		describeRead[RefInput, CommitmentItem](GetName, application.Operator),
		describeCommand[RefInput](DoneName),
		describeCommand[RefInput](ReopenName),
		describeCommand[RefInput](DismissName),
		describeCommand[UpdateInput](UpdateName),
		describeCommand[RefInput](DeleteName),
	}
}

var readScopes = []string{state.Tasks, state.Mail, state.Assistant, state.Knowledge, state.Review}

type shown struct {
	tasks, commitments bool
}

func visible(ctx context.Context) (shown, []string, error) {
	set := module.SetFrom(ctx)
	if set == nil || set.Current() == nil {
		return shown{}, nil, ErrNoModuleSet
	}
	snap := set.Current()
	var names []string
	for _, st := range snap.Statuses() {
		if st.State == module.Enabled {
			names = append(names, st.Name)
		}
	}
	return shown{tasks: snap.Visible(unit.Assistant), commitments: snap.Visible(unit.Signals)}, names, nil
}

func fromTask(t tasks.Task, links []tasks.Link) CommitmentItem {
	item := CommitmentItem{Ref: SourceTask + ":" + tasks.IDString(t.ID), Source: SourceTask, ID: t.ID, Side: SideTodo, Title: t.Title, Notes: t.Notes,
		Due: t.Due, Status: t.Status, Owner: OwnerMe, Priority: &t.Priority, RemindAt: t.RemindAt, CreatedTS: t.CreatedTS,
		UpdatedTS: &t.UpdatedTS, DoneTS: t.DoneTS, Links: links}
	if item.Links == nil {
		item.Links = []tasks.Link{}
	}
	switch t.Status {
	case tasks.StatusOpen:
		item.Actions = []string{ActionDone, ActionDismiss, ActionEdit, ActionDelete}
	default:
		item.Actions = []string{ActionReopen, ActionEdit, ActionDelete}
	}
	return item
}

func fromCommitment(ctx context.Context, q db.Querier, c signals.CommitmentRow) (CommitmentItem, error) {
	item := CommitmentItem{Ref: SourceCommitment + ":" + strconv.FormatInt(c.ID, 10), Source: SourceCommitment, ID: c.ID, Side: SideTodo, Title: c.Text,
		Due: c.Due, Status: c.Status, Owner: c.Owner, Counterpart: c.Counterpart, CreatedTS: c.CreatedTS,
		Links: []tasks.Link{{Kind: tasks.LinkMessage, ID: strconv.FormatInt(c.MessageID, 10), Title: c.Subject}}}
	if c.Owner == OwnerThem {
		item.Side = SideWaiting
	}
	if c.ProposalID != nil {
		link := tasks.Link{Kind: tasks.LinkProposal, ID: strconv.FormatInt(*c.ProposalID, 10)}
		p, err := proposals.Get(ctx, q, *c.ProposalID)
		if err != nil {
			return CommitmentItem{}, err
		}
		if p != nil {
			link.Title = &p.Title
		}
		item.Links = append(item.Links, link)
	}
	switch c.Status {
	case signals.Open:
		item.Actions = []string{ActionDone, ActionDismiss}
	case signals.Done, signals.Dismissed:
		item.Actions = []string{ActionReopen}
	default:
		item.Actions = []string{}
	}
	return item, nil
}

func commitmentMatches(c signals.CommitmentRow, f tasks.Filter) bool {
	due := ""
	if c.Due != nil && len(*c.Due) >= 10 {
		due = (*c.Due)[:10]
	} else if c.Due != nil {
		due = *c.Due
	}
	if f.DueFrom != "" && (due == "" || due < f.DueFrom) {
		return false
	}
	if f.DueTo != "" && (due == "" || due > f.DueTo) {
		return false
	}
	if f.LinkKind != "" {
		switch {
		case f.LinkKind == tasks.LinkMessage && f.LinkID == strconv.FormatInt(c.MessageID, 10):
		case f.LinkKind == tasks.LinkProposal && c.ProposalID != nil && f.LinkID == strconv.FormatInt(*c.ProposalID, 10):
		default:
			return false
		}
	}
	if text := strings.ToLower(strings.TrimSpace(f.Text)); text != "" && !strings.Contains(strings.ToLower(c.Text), text) {
		return false
	}
	return true
}

func items(ctx context.Context, q db.Querier, show shown, f tasks.Filter) ([]CommitmentItem, error) {
	var out []CommitmentItem
	if show.tasks {
		found, err := tasks.Page(ctx, q, f, "", -1, nil)
		if err != nil {
			return nil, err
		}
		for _, t := range found {
			links, err := tasksapp.LinksOf(ctx, q, t)
			if err != nil {
				return nil, err
			}
			out = append(out, fromTask(t, links))
		}
	}
	if show.commitments {
		rows, err := signals.CommitmentRows(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, c := range rows {
			if !commitmentMatches(c, f) {
				continue
			}
			item, err := fromCommitment(ctx, q, c)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
	}
	slices.SortFunc(out, func(a, b CommitmentItem) int { return a.Key().compare(b.Key()) })
	return out, nil
}

func count(c *CommitmentStatusCounts, status string) {
	switch status {
	case tasks.StatusOpen:
		c.Open++
	case tasks.StatusDone:
		c.Done++
	case tasks.StatusDismissed:
		c.Dismissed++
	}
}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[List], error) {
	show, names, err := visible(ctx)
	if err != nil {
		return application.Read[List]{}, err
	}
	if in.TasksOnly {
		show.commitments = false
	}
	if in.Limit <= 0 || in.Limit > MaxLimit {
		in.Limit = MaxLimit
	}
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[List]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, readScopes)
	if err != nil {
		return application.Read[List]{}, err
	}
	all, err := items(ctx, tx, show, in.Filter)
	if err != nil {
		return application.Read[List]{}, err
	}
	out := List{Items: []CommitmentItem{}}
	var targets []string
	for _, item := range all {
		if item.Side == SideWaiting {
			count(&out.Counts.Waiting, item.Status)
		} else {
			count(&out.Counts.Todo, item.Status)
		}
		if (in.Side != "" && item.Side != in.Side) || (in.Status != "" && item.Status != in.Status) {
			continue
		}
		if in.After != nil && item.Key().compare(*in.After) <= 0 {
			continue
		}
		if out.Next != nil {
			continue
		}
		if len(out.Items) == in.Limit {
			out.Next = new(out.Items[len(out.Items)-1].Key())
			continue
		}
		out.Items = append(out.Items, item)
		if item.Source == SourceTask {
			targets = append(targets, item.Ref)
		} else {
			out.Marks |= tool.Messages | tool.Untrusted
			if item.Counterpart != nil {
				out.Marks |= tool.PII
			}
		}
	}
	marks, err := threads.MarksOf(ctx, tx, targets)
	if err != nil {
		return application.Read[List]{}, err
	}
	out.Marks |= marks
	covered := struct {
		Input   ListInput `json:"input"`
		Modules []string  `json:"modules"`
	}{in, names}
	return application.Read[List]{Revision: application.Revision(state.Revision(string(ListName), counters, covered, "operator")), Data: out}, nil
}

func ParseRef(ref string) (string, int64, error) {
	source, raw, ok := strings.Cut(ref, ":")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil || id <= 0 || (source != SourceTask && source != SourceCommitment) {
		return "", 0, &application.Error{Code: application.Invalid, Message: "ref must be task:<id> or commitment:<id>",
			Fields: []application.Field{{Name: "ref", Code: "invalid", Message: "ref must be task:<id> or commitment:<id>"}}}
	}
	return source, id, nil
}

func unknown() error {
	return &application.Error{Code: application.NotFound, Message: "unknown commitment"}
}

func readOnly(verb string) error {
	return &application.Error{Code: application.Conflict, Reason: ReasonReadOnly, Message: "a commitment from mail cannot be " + verb + "; only its status changes"}
}

func wrongStatus(status, verb string) error {
	return &application.Error{Code: application.Conflict, Message: "a " + status + " item cannot be " + verb}
}

func (s *Service) item(ctx context.Context, q db.Querier, source string, id int64) (*CommitmentItem, error) {
	show, _, err := visible(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case source == SourceTask && show.tasks:
		t, err := tasks.Get(ctx, q, id)
		if err != nil || t == nil {
			return nil, err
		}
		links, err := tasksapp.LinksOf(ctx, q, *t)
		if err != nil {
			return nil, err
		}
		return new(fromTask(*t, links)), nil
	case source == SourceCommitment && show.commitments:
		c, err := signals.GetCommitment(ctx, q, id)
		if err != nil || c == nil {
			return nil, err
		}
		item, err := fromCommitment(ctx, q, *c)
		if err != nil {
			return nil, err
		}
		return &item, nil
	}
	return nil, nil
}

func (s *Service) Get(ctx context.Context, in RefInput) (application.Read[CommitmentItem], error) {
	source, id, err := ParseRef(in.Ref)
	if err != nil {
		return application.Read[CommitmentItem]{}, err
	}
	_, names, err := visible(ctx)
	if err != nil {
		return application.Read[CommitmentItem]{}, err
	}
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[CommitmentItem]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, readScopes)
	if err != nil {
		return application.Read[CommitmentItem]{}, err
	}
	item, err := s.item(ctx, tx, source, id)
	if err != nil {
		return application.Read[CommitmentItem]{}, err
	}
	if item == nil {
		return application.Read[CommitmentItem]{}, unknown()
	}
	covered := struct {
		Ref     string   `json:"ref"`
		Modules []string `json:"modules"`
	}{in.Ref, names}
	return application.Read[CommitmentItem]{Revision: application.Revision(state.Revision(string(GetName), counters, covered, "operator")), Data: *item}, nil
}

func (s *Service) resolve(ctx context.Context, ref string) (*CommitmentItem, error) {
	source, id, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	item, err := s.item(ctx, s.db.Read, source, id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, unknown()
	}
	return item, nil
}

func (s *Service) settle(ctx context.Context, ref string, effectIDs []int64, auditID int64) (application.Change[Changed], error) {
	out := application.Change[Changed]{Data: Changed{Ref: ref}, EffectIDs: effectIDs, AuditID: auditID}
	source, id, err := ParseRef(ref)
	if err != nil {
		return out, err
	}
	item, err := s.item(ctx, s.db.Read, source, id)
	if err != nil {
		return out, err
	}
	out.Data.Item = item
	if item != nil {
		out.Data.Status = item.Status
	}
	return out, nil
}

func (s *Service) Done(ctx context.Context, in RefInput) (application.Change[Changed], error) {
	return s.DoneAs(ctx, DoneName, in)
}

func (s *Service) DoneAs(ctx context.Context, name application.Name, in RefInput) (application.Change[Changed], error) {
	return s.transition(ctx, name, in, ActionDone, "done", signals.Done, signals.Open)
}

func (s *Service) Dismiss(ctx context.Context, in RefInput) (application.Change[Changed], error) {
	return s.DismissAs(ctx, DismissName, in)
}

func (s *Service) DismissAs(ctx context.Context, name application.Name, in RefInput) (application.Change[Changed], error) {
	return s.transition(ctx, name, in, ActionDismiss, "dismissed", signals.Dismissed, signals.Open)
}

func (s *Service) Reopen(ctx context.Context, in RefInput) (application.Change[Changed], error) {
	return s.transition(ctx, ReopenName, in, ActionReopen, "reopened", signals.Open, signals.Done, signals.Dismissed)
}

func (s *Service) transition(ctx context.Context, name application.Name, in RefInput, action, verb, to string, from ...string) (application.Change[Changed], error) {
	item, err := s.resolve(ctx, in.Ref)
	if err != nil {
		return s.refused(ctx, name, in.Ref, err)
	}
	if !slices.Contains(from, item.Status) {
		return s.refused(ctx, name, in.Ref, wrongStatus(item.Status, verb))
	}
	if item.Source == SourceCommitment {
		return s.commitmentStatus(ctx, name, in, item.ID, to, from)
	}
	id := tasksapp.IDInput{Source: in.Source, ID: item.ID}
	var changed application.Change[tasksapp.Changed]
	switch action {
	case ActionDone:
		changed, err = s.tasks.Done(ctx, id)
	case ActionDismiss:
		changed, err = s.tasks.Dismiss(ctx, id)
	default:
		changed, err = s.tasks.Reopen(ctx, id)
	}
	if err != nil {
		return application.Change[Changed]{}, err
	}
	return s.settle(ctx, in.Ref, changed.EffectIDs, changed.AuditID)
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (application.Change[Changed], error) {
	item, err := s.resolve(ctx, in.Ref)
	if err != nil {
		return s.refused(ctx, UpdateName, in.Ref, err)
	}
	if item.Source == SourceCommitment {
		return s.refused(ctx, UpdateName, in.Ref, readOnly("edited"))
	}
	changed, err := s.tasks.Update(ctx, tasksapp.UpdateInput{Source: in.Source, ID: item.ID, Title: in.Title, Notes: in.Notes, Due: in.Due,
		RemindAt: in.RemindAt, Priority: in.Priority, Link: in.Link, Clear: in.Clear, UpdatedTS: in.UpdatedTS})
	if err != nil {
		return application.Change[Changed]{}, err
	}
	return s.settle(ctx, in.Ref, changed.EffectIDs, changed.AuditID)
}

func (s *Service) Delete(ctx context.Context, in RefInput) (application.Change[Changed], error) {
	item, err := s.resolve(ctx, in.Ref)
	if err != nil {
		return s.refused(ctx, DeleteName, in.Ref, err)
	}
	if item.Source == SourceCommitment {
		return s.refused(ctx, DeleteName, in.Ref, readOnly("deleted"))
	}
	changed, err := s.tasks.Delete(ctx, tasksapp.IDInput{Source: in.Source, ID: item.ID})
	if err != nil {
		return application.Change[Changed]{}, err
	}
	return application.Change[Changed]{Data: Changed{Ref: in.Ref, Status: "deleted"}, EffectIDs: changed.EffectIDs, AuditID: changed.AuditID}, nil
}

func (s *Service) audit(ctx context.Context, tx *sql.Tx, name application.Name, ref string, outcome state.Outcome, effectID int64) (int64, error) {
	details := ""
	if effectID > 0 {
		details = jsonx.Dumps(map[string][]int64{"effect_ids": {effectID}}, jsonx.Opts{Compact: true})
	}
	return s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: outcome, Target: ref, DetailsJSON: details})
}

func (s *Service) refused(ctx context.Context, name application.Name, ref string, err error) (application.Change[Changed], error) {
	var e *application.Error
	if errors.As(err, &e) {
		outcome := state.Invalid
		switch e.Code {
		case application.NotFound:
			outcome = state.NotFound
		case application.Conflict:
			outcome = state.Conflict
		}
		if _, aerr := s.audit(ctx, nil, name, ref, outcome, 0); aerr != nil {
			return application.Change[Changed]{}, errors.Join(err, aerr)
		}
	}
	return application.Change[Changed]{}, err
}

func effect(src tasksapp.Source, target string) ledger.New {
	key := signals.UndoCommitmentStatus + ":" + ledger.NewKey()
	if src.ThreadID == 0 {
		return ledger.New{Origin: "local", Operation: signals.UndoCommitmentStatus, OperationKey: key, Target: target}
	}
	return ledger.New{Origin: "agent", Operation: signals.UndoCommitmentStatus, OperationKey: key, Target: target,
		ThreadID: &src.ThreadID, RunID: &src.RunID, ProposalID: src.ProposalID}
}

func encode(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (s *Service) commitmentStatus(ctx context.Context, name application.Name, in RefInput, id int64, to string, from []string) (application.Change[Changed], error) {
	var effectID, auditID int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		effect, err := s.ledger.ApplyLocalTx(ctx, tx, effect(in.Source, in.Ref), func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
			previous, err := signals.GetCommitment(ctx, tx, id)
			if err != nil {
				return ledger.Result{}, err
			}
			if previous == nil {
				return ledger.Result{}, unknown()
			}
			if !slices.Contains(from, previous.Status) {
				return ledger.Result{}, wrongStatus(previous.Status, "set to "+to)
			}
			if _, err := signals.SetCommitmentStatus(ctx, tx, id, to); err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, Before: encode(map[string]any{"status": previous.Status}), After: encode(map[string]any{"status": to}),
				Undo: encode(signals.CommitmentStatusUndo{Op: signals.UndoCommitmentStatus, ID: id, Status: previous.Status})}, nil
		})
		if err != nil {
			return err
		}
		effectID = effect.ID
		auditID, err = s.audit(ctx, tx, name, in.Ref, state.OK, effectID)
		return err
	})
	if err != nil {
		return s.refused(ctx, name, in.Ref, err)
	}
	return s.settle(ctx, in.Ref, []int64{effectID}, auditID)
}
