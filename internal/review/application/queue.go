package application

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/textx"
)

const (
	QueueName     application.Name = "review.queue"
	QueueItemName application.Name = "review.queue.item"
)

const (
	QueueAsk       = "ask"
	QueueProposal  = "proposal"
	QueueDraft     = "draft"
	QueueAttention = "attention"
)

const (
	WaitingAnswer     = "answer"
	WaitingPermission = "permission"
	WaitingApproval   = "approval"
	WaitingSend       = "send"
	WaitingResolve    = "resolve"
	WaitingReview     = "review"
	WaitingDue        = "due"
)

const (
	rankBlocksAssistant = iota
	rankDue
	rankWaiting
)

const summaryMax = 200

var ErrNoModuleSet = errors.New("review: no module set")

var queueScopes = []string{state.Review, state.Effects, state.Mail, state.Calendar, state.Tasks, state.Assistant, state.Settings, state.Connections, state.Attention, state.Ops}

var queueSkippedSources = []string{"proposals", "drafts"}

type QueueSource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type QueueItem struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind" api:"enum=ask|proposal|draft|attention"`
	Title      string      `json:"title"`
	Summary    string      `json:"summary"`
	Source     QueueSource `json:"source"`
	ArrivedAt  float64     `json:"arrived_at"`
	Due        *float64    `json:"due,omitempty"`
	WaitingFor string      `json:"waiting_for" api:"enum=answer|permission|approval|send|resolve|review|due"`
	Module     string      `json:"module"`
	rank       int
	at         float64
}

type QueueCounts struct {
	Ask       int `json:"ask"`
	Proposal  int `json:"proposal"`
	Draft     int `json:"draft"`
	Attention int `json:"attention"`
}

type QueueKey struct {
	Rank int     `json:"rank"`
	At   float64 `json:"at"`
	ID   string  `json:"id"`
}

func (k QueueKey) Valid() bool {
	return k.Rank >= rankBlocksAssistant && k.Rank <= rankWaiting && k.ID != ""
}

type QueueInput struct {
	Kind  string
	After *QueueKey
	Limit int
}

type Queue struct {
	Items  []QueueItem
	Next   *QueueKey
	Total  int
	Counts QueueCounts
}

type QueueItemInput struct {
	ID string
}

func (item QueueItem) Key() QueueKey { return QueueKey{Rank: item.rank, At: item.at, ID: item.ID} }

func (k QueueKey) compare(o QueueKey) int {
	return cmp.Or(cmp.Compare(k.Rank, o.Rank), cmp.Compare(k.At, o.At), strings.Compare(k.ID, o.ID))
}

func (s *Service) Queue(ctx context.Context, in QueueInput) (application.Read[Queue], error) {
	return queueRead(ctx, s, QueueName, in, func(items []QueueItem) (Queue, error) {
		out := Queue{Items: []QueueItem{}, Total: len(items), Counts: countQueue(items)}
		for _, item := range items {
			if in.Kind != "" && item.Kind != in.Kind {
				continue
			}
			if in.After != nil && item.Key().compare(*in.After) <= 0 {
				continue
			}
			if in.Limit > 0 && len(out.Items) == in.Limit {
				out.Next = new(out.Items[len(out.Items)-1].Key())
				break
			}
			out.Items = append(out.Items, item)
		}
		return out, nil
	})
}

func (s *Service) QueueItem(ctx context.Context, in QueueItemInput) (application.Read[QueueItem], error) {
	return queueRead(ctx, s, QueueItemName, in, func(items []QueueItem) (QueueItem, error) {
		for _, item := range items {
			if item.ID == in.ID {
				return item, nil
			}
		}
		return QueueItem{}, notFound("Nothing in the queue has that id.")
	})
}

func (s *Service) NeedsCount(ctx context.Context) (int, error) {
	items, err := s.queueItems(ctx, s.db.Read, queueNow())
	return len(items), err
}

func queueRead[T any](ctx context.Context, s *Service, name application.Name, input any, shape func([]QueueItem) (T, error)) (application.Read[T], error) {
	set := module.SetFrom(ctx)
	if set == nil || set.Current() == nil {
		return application.Read[T]{}, ErrNoModuleSet
	}
	now := queueNow()
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[T]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, queueScopes)
	if err != nil {
		return application.Read[T]{}, err
	}
	items, err := s.queueItems(ctx, tx, now)
	if err != nil {
		return application.Read[T]{}, err
	}
	data, err := shape(items)
	if err != nil {
		return application.Read[T]{}, err
	}
	var shown []string
	for _, st := range set.Current().Statuses() {
		if st.State == module.Enabled {
			shown = append(shown, st.Name)
		}
	}
	covered := struct {
		Input   any      `json:"input"`
		Modules []string `json:"modules"`
		Minute  int64    `json:"minute"`
	}{input, shown, int64(now) / 60}
	return application.Read[T]{Revision: application.Revision(state.Revision(string(name), counters, covered, "operator")), Data: data}, nil
}

func queueNow() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func countQueue(items []QueueItem) QueueCounts {
	var c QueueCounts
	for _, item := range items {
		switch item.Kind {
		case QueueAsk:
			c.Ask++
		case QueueProposal:
			c.Proposal++
		case QueueDraft:
			c.Draft++
		case QueueAttention:
			c.Attention++
		}
	}
	return c
}

func (s *Service) queueItems(ctx context.Context, q db.Querier, now float64) ([]QueueItem, error) {
	set := module.SetFrom(ctx)
	if set == nil || set.Current() == nil {
		return nil, ErrNoModuleSet
	}
	snap := set.Current()
	var items []QueueItem
	add := func(item QueueItem) {
		if !snap.Visible(item.Module) {
			return
		}
		item.Summary = oneLine(item.Summary)
		items = append(items, item)
	}

	pending, err := proposals.PendingProposals(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, p := range pending {
		kind := s.proposalKind(p.Kind)
		item := QueueItem{Kind: QueueProposal, Title: p.Title, Summary: cmp.Or(kind.Label, p.Kind), Source: QueueSource{Type: "proposal", ID: strconv.FormatInt(p.ID, 10)},
			ArrivedAt: p.CreatedTS, WaitingFor: WaitingApproval, Module: kind.Module, rank: rankWaiting, at: p.CreatedTS}
		if p.Kind == permission.Kind {
			item.Kind, item.WaitingFor, item.rank = QueueAsk, WaitingPermission, rankBlocksAssistant
		}
		add(item)
	}
	blocked, err := proposals.ListBlocked(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, p := range blocked {
		kind := s.proposalKind(p.Kind)
		add(QueueItem{Kind: QueueProposal, Title: p.Title, Summary: cmp.Or(kind.Label, p.Kind) + ": " + cmp.Or(p.BlockedError, proposals.UnknownOutcomeText),
			Source: QueueSource{Type: "proposal", ID: strconv.FormatInt(p.ID, 10)}, ArrivedAt: p.CreatedTS, WaitingFor: WaitingResolve, Module: kind.Module,
			rank: rankWaiting, at: p.CreatedTS})
	}

	for _, c := range s.kinds {
		if c.Kinds.Needs == nil || !snap.Visible(c.Module) {
			continue
		}
		needs, err := c.Kinds.Needs(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, d := range needs {
			item := QueueItem{Kind: QueueDraft, Title: cmp.Or(d.Subject, "(no subject)"), Summary: "To " + d.To, Source: QueueSource{Type: "draft", ID: strconv.FormatInt(d.ID, 10)},
				ArrivedAt: d.CreatedAt, WaitingFor: WaitingSend, Module: c.Module, rank: rankWaiting, at: d.CreatedAt}
			if d.Unknown {
				item.WaitingFor, item.Summary = WaitingResolve, "Send outcome unknown. To "+d.To
			}
			add(item)
		}
	}

	for _, owned := range snap.AttentionByModule() {
		var sources []attention.Source
		for _, source := range owned.Sources {
			if !slices.Contains(queueSkippedSources, source.Name()) {
				sources = append(sources, source)
			}
		}
		if len(sources) == 0 {
			continue
		}
		found, err := attention.Query(ctx, q, now, sources...)
		if err != nil {
			return nil, err
		}
		for _, a := range found.Items {
			item := QueueItem{Kind: QueueAttention, Title: a.Title, Summary: a.Detail, Source: QueueSource{Type: a.Kind, ID: a.SourceID},
				ArrivedAt: a.Created, Module: owned.Module}
			switch {
			case a.Kind == "question":
				item.Kind, item.WaitingFor, item.rank, item.at = QueueAsk, WaitingAnswer, rankBlocksAssistant, a.Created
			case a.Group == attention.Overdue || a.Group == attention.DueSoon:
				item.WaitingFor, item.rank, item.at = WaitingDue, rankDue, a.Due
				item.Due = new(a.Due)
			case a.Group == attention.NeedsDecision:
				item.WaitingFor, item.rank, item.at = WaitingReview, rankWaiting, a.Created
			default:
				continue
			}
			add(item)
		}
	}

	for i := range items {
		items[i].ID = items[i].Source.Type + ":" + items[i].Source.ID
	}
	slices.SortFunc(items, func(a, b QueueItem) int { return a.Key().compare(b.Key()) })
	return items, nil
}

func (s *Service) proposalKind(name string) proposals.Kind {
	if s.registry == nil {
		return proposals.Kind{}
	}
	kind, _ := s.registry.Kind(name)
	return kind
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if clipped := textx.Clip(s, summaryMax); clipped != s {
		return clipped + "…"
	}
	return s
}
