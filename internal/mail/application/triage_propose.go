package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
)

const ProposeTriageName application.Name = "mail.propose-triage"

type TriageRequest struct {
	ID     int64
	Action string
	Label  string
}
type ProposeTriageInput struct{ Items []TriageRequest }
type ProposeTriageResult struct{ ProposalID int64 }

func DescribeProposeTriage() application.Definition {
	return application.Describe[ProposeTriageInput, ProposeTriageResult](ProposeTriageName, application.Command, []application.Authority{application.Agent}, application.NoEffect, true)
}

func triageInvalid(message string) error {
	return &application.Error{Code: application.Invalid, Message: message}
}

func (s *Service) ProposeTriage(ctx context.Context, in ProposeTriageInput) (application.Change[ProposeTriageResult], error) {
	id, err := s.proposeTriage(ctx, in.Items)
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	target := ""
	if id != 0 {
		target = strconv.FormatInt(id, 10)
	}
	auditID := s.writeAudit(ctx, ProposeTriageName, target, outcome, nil)
	if err != nil {
		return application.Change[ProposeTriageResult]{}, err
	}
	return application.Change[ProposeTriageResult]{Data: ProposeTriageResult{ProposalID: id}, AuditID: auditID}, nil
}

func (s *Service) proposeTriage(ctx context.Context, requests []TriageRequest) (int64, error) {
	ids := make([]int64, len(requests))
	for i, req := range requests {
		ids[i] = req.ID
	}
	snaps, err := mailops.Snapshots(ctx, s.db.Read, ids)
	if err != nil {
		return 0, err
	}
	if len(snaps) != len(requests) {
		return 0, triageInvalid("one or more messages are unknown")
	}
	byID := map[int64]mailops.Snapshot{}
	for _, snap := range snaps {
		byID[snap.ID] = snap
	}
	found, err := mail.MessageInfos(ctx, s.db.Read, ids)
	if err != nil {
		return 0, err
	}
	owners, subjects, senders, dates := map[int64]string{}, map[int64]string{}, map[int64]string{}, map[int64]float64{}
	for _, m := range found {
		owners[m.ID], subjects[m.ID], senders[m.ID], dates[m.ID] = m.Account, m.Subject, m.FromAddr, m.DateTS
	}
	var account string
	items := make([]executors.TriageItem, 0, len(requests))
	preview := make([]any, 0, len(requests))
	for _, req := range requests {
		snap, ok := byID[req.ID]
		if !ok {
			return 0, triageInvalid("one or more messages are unknown")
		}
		owner := owners[req.ID]
		if account == "" {
			account = owner
		} else if account != owner {
			return 0, triageInvalid("all items must belong to one account")
		}
		item := executors.TriageItem{Snapshot: snap, Action: req.Action, Label: req.Label}
		items = append(items, item)
		preview = append(preview, map[string]any{"id": req.ID, "subject": subjects[req.ID], "from_addr": senders[req.ID], "date_ts": dates[req.ID], "action": req.Action, "label": req.Label})
	}
	dicts := make([]any, len(items))
	for i, item := range items {
		dicts[i] = item.Dict()
	}
	body := map[string]any{"items": dicts}
	view := map[string]any{"count": len(items), "messages": preview}
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256(append([]byte(account+"\x00"), encoded...))
	id, _, err := proposals.Create(ctx, s.db, executors.MailTriage, &account,
		fmt.Sprintf("Triage %d messages", len(items)), body, view, hex.EncodeToString(sum[:]), s.now())
	return id, err
}
