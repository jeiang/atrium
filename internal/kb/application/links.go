package application

import (
	"context"
	"database/sql"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tasks"
)

const (
	LinksName  application.Name = "knowledge.links"
	LinkName   application.Name = "knowledge.link"
	UnlinkName application.Name = "knowledge.unlink"

	EndLive        = "live"
	EndRetracted   = "retracted"
	EndDone        = "done"
	EndDismissed   = "dismissed"
	EndMissing     = "missing"
	EndUnavailable = "unavailable"
	EndArchived    = "archived"

	DirectionOutgoing = "outgoing"
	DirectionIncoming = "incoming"
)

var linkScopes = []string{state.Knowledge, state.Tasks, state.Assistant, state.Effects, state.Settings}

type LinkInput struct {
	RecordID   int64
	TargetKind string
	TargetID   string
	Relation   string
}

type LinkIDInput struct {
	ID int64
}

type LinksInput struct {
	ID int64
}

type Endpoint struct {
	Kind       string
	ID         string
	Title      string
	RecordKind string
	State      string
	Address    string
}

type Link struct {
	ID        int64
	Relation  string
	Writer    string
	RunID     *int64
	CreatedAt float64
	Retracted *float64
	Cited     bool
	Source    Endpoint
	Target    Endpoint
}

type Links struct {
	Record   int64
	Outgoing []Link
	Incoming []Link
}

func DescribeLinks() application.Definition {
	return application.Describe[LinksInput, Links](LinksName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeLink() application.Definition {
	return describeOperator[LinkInput, Link](LinkName, application.LocalUndoable)
}

func DescribeUnlink() application.Definition {
	return describeOperator[LinkIDInput, Link](UnlinkName, application.LocalUndoable)
}

type resolver struct {
	ctx    context.Context
	q      db.Querier
	loaded bool
	ready  bool
	cards  map[string]contacts.Card
}

func (r *resolver) contacts() error {
	if r.loaded {
		return nil
	}
	r.loaded = true
	src, err := contacts.Resolve(r.ctx, r.q)
	if err != nil || src.State != contacts.Ready {
		return err
	}
	r.ready, r.cards = true, map[string]contacts.Card{}
	for _, c := range contacts.Load(src.Dir) {
		if c.UID != "" {
			r.cards[c.UID] = c
		}
	}
	return nil
}

func (r *resolver) endpoint(kind, id string) (Endpoint, error) {
	out := Endpoint{Kind: kind, ID: id, State: EndMissing}
	switch kind {
	case kb.TargetRecord:
		out.Address = "/knowledge/" + id
		n, ok := kb.RecordTarget(id)
		if !ok {
			return out, nil
		}
		rec, err := kb.Get(r.ctx, r.q, n)
		if err != nil || rec == nil {
			return out, err
		}
		out.Title, out.RecordKind, out.State = rec.Title, kb.KindOf(*rec), EndLive
		if rec.Retracted() {
			out.State = EndRetracted
		}
	case kb.TargetTask:
		out.Address = "/tasks/" + id
		n, ok := kb.RecordTarget(id)
		if !ok {
			return out, nil
		}
		t, err := tasks.Get(r.ctx, r.q, n)
		if err != nil || t == nil {
			return out, err
		}
		out.Title, out.State = t.Title, EndLive
		switch t.Status {
		case tasks.StatusDone:
			out.State = EndDone
		case tasks.StatusDismissed:
			out.State = EndDismissed
		}
	case kb.TargetContact:
		out.Address = "/contacts/" + url.PathEscape(id)
		if err := r.contacts(); err != nil {
			return out, err
		}
		if !r.ready {
			out.State = EndUnavailable
			return out, nil
		}
		if c, ok := r.cards[id]; ok {
			out.Title, out.State = c.Name, EndLive
		}
	case kb.TargetSpace:
		out.Address = "/knowledge/spaces/" + id
		n, ok := kb.RecordTarget(id)
		if !ok {
			return out, nil
		}
		p, err := projects.Get(r.ctx, r.q, n)
		if err != nil || p == nil {
			return out, err
		}
		out.Title, out.State = p.Name, EndLive
		if p.Archived {
			out.State = EndArchived
		}
	}
	return out, nil
}

func (r *resolver) link(l kb.Link) (Link, error) {
	src, err := r.endpoint(kb.TargetRecord, strconv.FormatInt(l.RecordID, 10))
	if err != nil {
		return Link{}, err
	}
	dst, err := r.endpoint(l.TargetKind, l.TargetID)
	if err != nil {
		return Link{}, err
	}
	return Link{ID: l.ID, Relation: l.Relation, Writer: l.Writer, RunID: l.RunID, CreatedAt: l.CreatedAt, Retracted: l.RetractedAt, Source: src, Target: dst}, nil
}

func (r *resolver) links(found []kb.Link) ([]Link, error) {
	out := make([]Link, len(found))
	for i, l := range found {
		v, err := r.link(l)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func recordEnd(r kb.Record) Endpoint {
	return Endpoint{Kind: kb.TargetRecord, ID: strconv.FormatInt(r.ID, 10), Title: r.Title, RecordKind: kb.KindOf(r), State: EndLive,
		Address: "/knowledge/" + strconv.FormatInt(r.ID, 10)}
}

func citedLink(from, to kb.Record) Link {
	writer := kb.LinkerAssistant
	if from.Writer == kb.WriterOperator {
		writer = kb.LinkerOperator
	}
	return Link{Relation: kb.RelationSeeAlso, Writer: writer, CreatedAt: from.UpdatedAt, Cited: true, Source: recordEnd(from), Target: recordEnd(to)}
}

func linksOf(ctx context.Context, q db.Querier, rec kb.Record, origin string) (Links, error) {
	r := &resolver{ctx: ctx, q: q}
	id := strconv.FormatInt(rec.ID, 10)
	from, err := kb.LinksFrom(ctx, q, rec.ID)
	if err != nil {
		return Links{}, err
	}
	to, err := kb.LinksTo(ctx, q, kb.TargetRecord, id)
	if err != nil {
		return Links{}, err
	}
	cites, err := kb.CitedRecords(ctx, q, rec, origin)
	if err != nil {
		return Links{}, err
	}
	citing, err := kb.CitingRecords(ctx, q, rec, origin)
	if err != nil {
		return Links{}, err
	}
	out := Links{Record: rec.ID}
	if out.Outgoing, err = r.links(from); err != nil {
		return Links{}, err
	}
	if out.Incoming, err = r.links(to); err != nil {
		return Links{}, err
	}
	stored := map[[2]string]bool{}
	for _, l := range from {
		if l.TargetKind == kb.TargetRecord {
			stored[[2]string{id, l.TargetID}] = true
		}
	}
	for _, l := range to {
		stored[[2]string{strconv.FormatInt(l.RecordID, 10), id}] = true
	}
	for _, c := range cites {
		if !stored[[2]string{id, strconv.FormatInt(c.ID, 10)}] {
			out.Outgoing = append(out.Outgoing, citedLink(rec, c))
		}
	}
	for _, c := range citing {
		if !stored[[2]string{strconv.FormatInt(c.ID, 10), id}] {
			out.Incoming = append(out.Incoming, citedLink(c, rec))
		}
	}
	return out, nil
}

func origin(ctx context.Context) string {
	return config.HostValueFrom(ctx, config.KeyPublicURL)
}

func (s *Service) Links(ctx context.Context, in LinksInput) (application.Read[Links], error) {
	var out Links
	rev, err := s.snapshot(ctx, string(LinksName), linkScopes, in, func(q db.Querier) error {
		rec, err := load(ctx, q, in.ID)
		if err != nil {
			return err
		}
		out, err = linksOf(ctx, q, rec, origin(ctx))
		return err
	})
	if err != nil {
		return application.Read[Links]{}, err
	}
	return application.Read[Links]{Revision: rev, Data: out}, nil
}

func (in LinkInput) normalize() (LinkInput, error) {
	in.TargetKind, in.TargetID, in.Relation = strings.TrimSpace(in.TargetKind), strings.TrimSpace(in.TargetID), strings.TrimSpace(in.Relation)
	if !slices.Contains(kb.LinkTargets, in.TargetKind) {
		return in, invalid("target_kind", "target_kind must be record, task, contact or space")
	}
	if !slices.Contains(kb.Relations, in.Relation) {
		return in, invalid("relation", "relation must be one of "+strings.Join(kb.Relations, ", "))
	}
	if in.TargetID == "" {
		return in, invalid("target_id", "target_id is required")
	}
	if in.TargetKind != kb.TargetContact {
		if _, ok := kb.RecordTarget(in.TargetID); !ok {
			return in, invalid("target_id", "target_id must be a positive id")
		}
	}
	return in, nil
}

func (s *Service) checkTarget(ctx context.Context, q db.Querier, in LinkInput) error {
	if in.TargetKind == kb.TargetRecord {
		return nil
	}
	r := &resolver{ctx: ctx, q: q}
	end, err := r.endpoint(in.TargetKind, in.TargetID)
	switch {
	case err != nil:
		return err
	case end.State == EndUnavailable:
		return kb.Refused("contacts are not configured")
	case end.State == EndMissing:
		return kb.Refused(in.TargetKind + " " + in.TargetID + " is unknown")
	}
	return nil
}

func (s *Service) Link(ctx context.Context, in LinkInput) (application.Change[Link], error) {
	in, err := in.normalize()
	if err != nil {
		return application.Change[Link]{}, s.reject(ctx, LinkName, in.RecordID, err)
	}
	input := kb.LinkInput{RecordID: in.RecordID, TargetKind: in.TargetKind, TargetID: in.TargetID, Relation: in.Relation, Writer: kb.LinkerOperator}
	now := s.now()
	d, err := transact(ctx, s, LinkName, "kb_link", in.RecordID, func(ctx context.Context, tx *sql.Tx) (kb.Link, bool, error) {
		live, err := kb.LiveLink(ctx, tx, input)
		if err != nil || live != nil {
			return derefLink(live), live != nil, err
		}
		return kb.Link{}, false, s.checkTarget(ctx, tx, in)
	}, func(ctx context.Context, tx *sql.Tx) (kb.Link, ledger.Result, error) {
		return kb.LinkTx(ctx, tx, input, now)
	}, func(l kb.Link) int64 { return l.RecordID })
	if err != nil {
		return application.Change[Link]{}, err
	}
	return s.linkChange(ctx, d)
}

func (s *Service) Unlink(ctx context.Context, in LinkIDInput) (application.Change[Link], error) {
	found, err := kb.GetLink(ctx, s.db.Read, in.ID)
	if err != nil {
		return application.Change[Link]{}, err
	}
	if found == nil {
		return application.Change[Link]{}, s.reject(ctx, UnlinkName, 0, notFound("unknown knowledge link"))
	}
	now := s.now()
	d, err := transact(ctx, s, UnlinkName, "kb_unlink", found.RecordID, func(ctx context.Context, tx *sql.Tx) (kb.Link, bool, error) {
		l, err := kb.GetLink(ctx, tx, in.ID)
		if err == nil && l == nil {
			err = notFound("unknown knowledge link")
		}
		if err != nil {
			return kb.Link{}, false, err
		}
		return *l, l.Retracted(), nil
	}, func(ctx context.Context, tx *sql.Tx) (kb.Link, ledger.Result, error) {
		return kb.UnlinkTx(ctx, tx, in.ID, now)
	}, func(l kb.Link) int64 { return l.RecordID })
	if err != nil {
		return application.Change[Link]{}, err
	}
	return s.linkChange(ctx, d)
}

func derefLink(l *kb.Link) kb.Link {
	if l == nil {
		return kb.Link{}
	}
	return *l
}

func (s *Service) linkChange(ctx context.Context, d done[kb.Link]) (application.Change[Link], error) {
	view, err := (&resolver{ctx: ctx, q: s.db.Read}).link(d.value)
	if err != nil {
		return application.Change[Link]{}, err
	}
	return changeOf(d, view), nil
}
