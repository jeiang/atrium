package coremodule

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

type ApproveRequest struct {
	ProposalID int64           `path:"proposal_id" json:"-" api:"min=1"`
	Selection  json.RawMessage `json:"selection,omitempty"`
}

type DenyRequest struct {
	ProposalID int64 `path:"proposal_id" json:"-" api:"min=1"`
	Forever    bool  `json:"forever,omitempty"`
}

type ProposalResolveRequest struct {
	ProposalID int64  `path:"proposal_id" json:"-" api:"min=1"`
	Outcome    string `json:"outcome,omitempty"`
}

type EffectResolveRequest struct {
	EffectID int64           `path:"effect_id" json:"-" api:"min=1"`
	Outcome  string          `json:"outcome,omitempty"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

type EffectUndoRequest struct {
	EffectID int64 `path:"effect_id" json:"-" api:"min=1"`
}

type UndoRequest struct {
	EntryID string `path:"entry_id" json:"-"`
}

type Approved struct {
	Approval reviewpages.Approval `json:"approval"`
	Error    *string              `json:"error,omitempty"`
	Outcome  string               `json:"outcome"`
	Summary  *string              `json:"summary,omitempty"`
}

type Decided struct {
	Approval reviewpages.Approval `json:"approval"`
}

type Undone struct {
	Entry  *reviewapp.UndoEntry `json:"entry"`
	Error  *string              `json:"error,omitempty"`
	Undone bool                 `json:"undone"`
}

type EffectResolved struct {
	Effect *reviewapp.Effect `json:"effect"`
}

type EffectUndone struct {
	Effect *reviewapp.Effect `json:"effect"`
	Undo   *reviewapp.Effect `json:"undo"`
	Undone bool              `json:"undone"`
}

func refusal(err error, code, missing string, extra map[string]any) error {
	var e *application.Error
	if !errors.As(err, &e) {
		return err
	}
	out := *e
	out.Cause = nil
	switch e.Code {
	case application.NotFound:
		return &application.Error{Code: application.NotFound, Reason: "not_found", Message: missing}
	case application.Invalid:
		out.Reason = "invalid"
		for _, f := range e.Fields {
			if f.Name == "selection" {
				out.Reason = "bad_selection"
			}
		}
	case application.Refused, application.Conflict:
		refused := &application.Error{Code: application.Conflict, Reason: code, Message: e.Message}
		if extra != nil {
			return web.WithBody(refused, extra)
		}
		return refused
	}
	return &out
}

func unknown(message string) error {
	return &application.Error{Code: application.NotFound, Reason: "not_found", Message: message}
}

func reviewWireRoutes(d *web.Deps, svc func() *reviewapp.Service) []application.Binding {
	const missingApproval, missingEntry, missingEffect = "Unknown approval.", "Unknown undo entry.", "Unknown effect."
	decided := func(ctx context.Context, id int64) (Decided, error) {
		found, err := svc().Proposal(ctx, reviewapp.ProposalInput{ID: id})
		if err != nil {
			return Decided{}, err
		}
		return Decided{Approval: reviewpages.ApprovalOf(found.Data)}, nil
	}
	proposals := func(response reflect.Type, codes ...application.Code) application.APIShape {
		return application.APIShape{Area: "proposals", OptionalBody: true, Response: response, Errors: codes}
	}
	return []application.Binding{
		api.Handle(d, application.Binding{Kind: application.API, Target: "GET /pending", Uses: []application.Name{reviewapp.PendingName}, API: &application.APIShape{Area: "review", Request: reflect.TypeFor[struct{}](), Response: reflect.TypeFor[reviewpages.Pending](), Body: application.BodyNone, Success: 200}},
			func(w http.ResponseWriter, r *http.Request) { reviewpages.PendingRead(w, r, svc()) }),
		api.Command(d, reviewapp.DescribeOne(reviewapp.ApproveName), proposals(reflect.TypeFor[Approved](), application.NotFound, application.Invalid, application.Conflict),
			"POST /approvals/{proposal_id}/approve", func(ctx context.Context, in ApproveRequest) (application.Change[Approved], error) {
				input := reviewapp.ApproveInput{ProposalID: in.ProposalID}
				if len(in.Selection) > 0 && string(in.Selection) != "null" {
					picked, err := jsonx.Loads(in.Selection)
					if err != nil {
						return application.Change[Approved]{}, err
					}
					input.SelectionValue = picked
				}
				s := svc()
				done, err := s.Approve(ctx, input)
				if err != nil {
					var extra map[string]any
					if done.Data.Proposal != nil {
						extra = map[string]any{"approval": reviewpages.ApprovalOf(s.CardOf(done.Data.Proposal)), "outcome": "failed"}
					}
					return application.Change[Approved]{}, refusal(err, "not_approved", missingApproval, extra)
				}
				out := Approved{Approval: reviewpages.ApprovalOf(s.CardOf(done.Data.Proposal)), Outcome: "failed"}
				if done.Data.Applied {
					out.Outcome = "applied"
					out.Summary = new(reviewpages.ResultString(done.Data.Proposal.Result, "summary"))
				} else {
					out.Error = new(reviewpages.ResultString(done.Data.Proposal.Result, "error"))
				}
				return application.Change[Approved]{Data: out, AuditID: done.AuditID}, nil
			}),
		api.Command(d, reviewapp.DescribeOne(reviewapp.DismissName), proposals(reflect.TypeFor[Decided](), application.NotFound, application.Conflict),
			"POST /approvals/{proposal_id}/deny", func(ctx context.Context, in DenyRequest) (application.Change[Decided], error) {
				done, err := svc().Dismiss(ctx, reviewapp.DismissInput{ProposalID: in.ProposalID, Suppress: in.Forever})
				if err != nil {
					return application.Change[Decided]{}, refusal(err, "not_dismissed", missingApproval, nil)
				}
				out, err := decided(ctx, in.ProposalID)
				return application.Change[Decided]{Data: out, AuditID: done.AuditID}, err
			}),
		api.Command(d, reviewapp.DescribeOne(reviewapp.ResolveName), proposals(reflect.TypeFor[Decided](), application.NotFound, application.Invalid, application.Conflict),
			"POST /approvals/{proposal_id}/resolve", func(ctx context.Context, in ProposalResolveRequest) (application.Change[Decided], error) {
				done, err := svc().Resolve(ctx, reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.ProposalTarget, ID: in.ProposalID}, Outcome: in.Outcome})
				if err != nil {
					return application.Change[Decided]{}, refusal(err, "not_resolved", missingApproval, nil)
				}
				out, err := decided(ctx, in.ProposalID)
				return application.Change[Decided]{Data: out, AuditID: done.AuditID}, err
			}),
		api.Handle(d, application.Binding{Kind: application.API, Target: "GET /undo", Uses: []application.Name{reviewapp.UndoableName}, API: &application.APIShape{Area: "activity", Request: reflect.TypeFor[reviewpages.UndoFeedRequest](), Response: reflect.TypeFor[web.List[reviewapp.UndoEntry]](), Body: application.BodyNone, Success: 200, Errors: []application.Code{application.Invalid}}},
			func(w http.ResponseWriter, r *http.Request) { reviewpages.UndoFeed(w, r, svc()) }),
		api.Command(d, reviewapp.DescribeOne(reviewapp.UndoName), application.APIShape{Area: "activity", Body: application.BodyNone, IgnoreBody: true, Response: reflect.TypeFor[Undone](), Errors: []application.Code{application.NotFound, application.Conflict}},
			"POST /undo/{entry_id}", func(ctx context.Context, in UndoRequest) (application.Change[Undone], error) {
				s := svc()
				source, raw, _ := strings.Cut(in.EntryID, ":")
				id, perr := strconv.ParseInt(raw, 10, 64)
				if perr != nil || id < 1 {
					return application.Change[Undone]{}, unknown(missingEntry)
				}
				switch source {
				case "write":
					known, err := undoEffectEntry(ctx, s, id)
					if err != nil {
						return application.Change[Undone]{}, err
					}
					if known == nil {
						return application.Change[Undone]{}, unknown(missingEntry)
					}
					done, err := s.Undo(ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: id}})
					if err != nil {
						return application.Change[Undone]{}, refusal(err, "not_undone", missingEntry, nil)
					}
					entry, err := undoEffectEntry(ctx, s, id)
					return application.Change[Undone]{Data: Undone{Entry: entry, Undone: true}, AuditID: done.AuditID}, err
				case "proposal":
					done, err := s.Undo(ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.ProposalTarget, ID: id}})
					outcome := done.Data.Outcome
					if err != nil {
						var extra map[string]any
						if done.Data.Proposal != nil && (len(outcome.Outcomes) > 0 || outcome.SyncError != nil) {
							view := outcome.Dict()
							extra = map[string]any{"entry": reviewapp.UndoProposalEntry(done.Data.Proposal), "outcomes": view.Outcomes, "sync_error": view.SyncError}
						}
						return application.Change[Undone]{}, refusal(err, "not_undone", missingEntry, extra)
					}
					entry := reviewapp.UndoProposalEntry(done.Data.Proposal)
					out := Undone{Entry: &entry, Undone: outcome.Undone}
					if !outcome.Undone {
						for _, o := range outcome.Outcomes {
							if o["ok"] != true {
								text, _ := o["error"].(string)
								out.Error = &text
								break
							}
						}
					}
					return application.Change[Undone]{Data: out, AuditID: done.AuditID}, nil
				}
				return application.Change[Undone]{}, unknown(missingEntry)
			}),
		api.Handle(d, application.Binding{Kind: application.API, Target: "GET /effects", Uses: []application.Name{reviewapp.EffectsName}, API: &application.APIShape{Area: "activity", Request: reflect.TypeFor[reviewpages.EffectsRequest](), Response: reflect.TypeFor[web.List[reviewapp.Effect]](), Body: application.BodyNone, Success: 200, Errors: []application.Code{application.Invalid}}},
			func(w http.ResponseWriter, r *http.Request) { reviewpages.EffectsRead(w, r, svc()) }),
		api.Handle(d, application.Binding{Kind: application.API, Target: "GET /effects/{effect_id}", Uses: []application.Name{reviewapp.EffectName}, API: &application.APIShape{Area: "activity", Request: reflect.TypeFor[reviewpages.EffectRequest](), Response: reflect.TypeFor[reviewapp.Effect](), Body: application.BodyNone, Success: 200, Errors: []application.Code{application.NotFound}}},
			func(w http.ResponseWriter, r *http.Request) { reviewpages.EffectRead(w, r, svc()) }),
		api.Command(d, reviewapp.DescribeOne(reviewapp.ResolveName), application.APIShape{Area: "activity", ID: "review.resolve-effect", OptionalBody: true, Response: reflect.TypeFor[EffectResolved](), Errors: []application.Code{application.NotFound, application.Invalid, application.Conflict}},
			"POST /effects/{effect_id}/resolve", func(ctx context.Context, in EffectResolveRequest) (application.Change[EffectResolved], error) {
				done, err := svc().Resolve(ctx, reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: in.EffectID}, Outcome: in.Outcome, Evidence: in.Evidence})
				if err != nil {
					return application.Change[EffectResolved]{}, refusal(err, "not_resolved", missingEffect, nil)
				}
				return application.Change[EffectResolved]{Data: EffectResolved{Effect: done.Data.Effect}, AuditID: done.AuditID}, nil
			}),
		api.Command(d, reviewapp.DescribeOne(reviewapp.UndoName), application.APIShape{Area: "activity", ID: "review.undo-effect", Body: application.BodyNone, IgnoreBody: true, Response: reflect.TypeFor[EffectUndone](), Errors: []application.Code{application.NotFound, application.Conflict}},
			"POST /effects/{effect_id}/undo", func(ctx context.Context, in EffectUndoRequest) (application.Change[EffectUndone], error) {
				done, err := svc().Undo(ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: in.EffectID}})
				if err != nil {
					return application.Change[EffectUndone]{}, refusal(err, "not_undone", missingEffect, nil)
				}
				return application.Change[EffectUndone]{Data: EffectUndone{Effect: done.Data.Effect, Undo: done.Data.Undo, Undone: true}, AuditID: done.AuditID}, nil
			}),
	}
}

func undoEffectEntry(ctx context.Context, s *reviewapp.Service, id int64) (*reviewapp.UndoEntry, error) {
	e, err := s.StoredEffect(ctx, id)
	var missing *reviewapp.EffectNotFound
	if errors.As(err, &missing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry, err := reviewapp.UndoEffectEntry(e)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}
