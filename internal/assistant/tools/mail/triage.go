package mailtools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
)

func RegisterProposalKinds() {
	proposetools.Register(executors.MailTriage,
		"{items: [{id, action, label?, due?}]}: propose archive, label or snooze for a batch of messages; one proposal per run",
		proposeTriage)
}

type triageRequest struct {
	ID     int64
	Action string
	Label  string
	Due    string
}

func triageRequests(payload map[string]any) ([]triageRequest, error) {
	raw, ok := payload["items"]
	if !ok {
		return nil, tool.Error("items is required")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, tool.Error("items must be a non-empty array")
	}
	out := make([]triageRequest, 0, len(list))
	for _, entry := range list {
		item, ok := entry.(map[string]any)
		if !ok {
			return nil, tool.Error("each item must be an object")
		}
		id, err := intArg(item["id"], "each item needs a message id")
		if err != nil {
			return nil, err
		}
		action, ok := item["action"].(string)
		if !ok {
			return nil, tool.Error("each item needs an action")
		}
		switch action {
		case executors.TriageArchive, executors.TriageLabel, executors.TriageSnooze:
		default:
			return nil, tool.Error("action must be archive, label or snooze")
		}
		req := triageRequest{ID: id, Action: action}
		if label, ok := item["label"].(string); ok {
			req.Label = strings.TrimSpace(label)
		}
		if req.Action == executors.TriageLabel && req.Label == "" {
			return nil, tool.Error("a label action needs a label")
		}
		if due, ok := item["due"].(string); ok {
			req.Due = strings.TrimSpace(due)
		}
		if req.Action == executors.TriageSnooze && req.Due == "" {
			return nil, tool.Error("a snooze action needs a due time")
		}
		out = append(out, req)
	}
	return out, nil
}

func dueTime(ctx context.Context, value string, env config.Env) (float64, error) {
	loc, err := zone(ctx, env)
	if err != nil {
		return 0, err
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return float64(t.UnixNano()) / 1e9, nil
		}
	}
	return 0, tool.Error("due must be an RFC 3339 time or YYYY-MM-DD")
}

func proposeTriage(ctx context.Context, tc *tool.Context, payload map[string]any) (*int64, error) {
	requests, err := triageRequests(payload)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(requests))
	for i, req := range requests {
		ids[i] = req.ID
	}
	snaps, err := mailops.Snapshots(ctx, tc.DB.Read, ids)
	if err != nil {
		return nil, err
	}
	if len(snaps) != len(requests) {
		return nil, tool.Error("one or more messages are unknown")
	}
	byID := map[int64]mailops.Snapshot{}
	for _, snap := range snaps {
		byID[snap.ID] = snap
	}
	owners, subjects, senders, dates, err := messageRows(ctx, tc, ids)
	if err != nil {
		return nil, err
	}
	var account string
	items := make([]executors.TriageItem, 0, len(requests))
	preview := make([]any, 0, len(requests))
	for _, req := range requests {
		snap, ok := byID[req.ID]
		if !ok {
			return nil, tool.Error("one or more messages are unknown")
		}
		owner := owners[req.ID]
		if account == "" {
			account = owner
		} else if account != owner {
			return nil, tool.Error("all items must belong to one account")
		}
		item := executors.TriageItem{Snapshot: snap, Action: req.Action, Label: req.Label}
		if req.Action == executors.TriageSnooze {
			if item.DueTS, err = dueTime(ctx, req.Due, tc.Env); err != nil {
				return nil, err
			}
		}
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
		return nil, err
	}
	sum := sha256.Sum256(append([]byte(account+"\x00"), encoded...))
	id, _, err := proposals.Create(ctx, tc.DB, executors.MailTriage, &account,
		fmt.Sprintf("Triage %d messages", len(items)), body, view, hex.EncodeToString(sum[:]), tc.Now())
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func messageRows(ctx context.Context, tc *tool.Context, ids []int64) (map[int64]string, map[int64]string, map[int64]string, map[int64]float64, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := tc.DB.Read.QueryContext(ctx, "SELECT id, account, subject, from_addr, date_ts FROM messages WHERE id IN (?"+strings.Repeat(",?", len(ids)-1)+")", args...)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	owners, subjects, senders, dates := map[int64]string{}, map[int64]string{}, map[int64]string{}, map[int64]float64{}
	for rows.Next() {
		var id int64
		var account, subject, sender string
		var date float64
		if err := rows.Scan(&id, &account, &subject, &sender, &date); err != nil {
			return nil, nil, nil, nil, err
		}
		owners[id], subjects[id], senders[id], dates[id] = account, subject, sender, date
	}
	return owners, subjects, senders, dates, rows.Err()
}
