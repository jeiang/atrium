package mailtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
)

var trackPackageSchema = json.RawMessage(`{"type":"object","properties":{"package_id":{"type":"integer"},"tracking_number":{"type":"string"},"carrier":{"type":"string"},"destination_country":{"type":"string"},"destination_postcode":{"type":"string"}},"additionalProperties":false}`)

func trackingTools() []tool.Tool {
	return []tool.Tool{approval.Gate(tool.Tool{
		Name: "track_package",
		Description: "Ask the operator to approve registering a package's tracking number with Ship24 (a paid lookup); nothing is sent until they approve the card. " +
			"Use the id from the packages tool, or give a tracking_number. Undoable.",
		InputSchema: trackPackageSchema,
		Effect:      tool.Propose, Undo: tracking.UndoUntrack,
		Annotated: true,
		Flags:     tool.Flags{ExternalEffect: true, Cost: true},
		Contexts:  tool.Kinds{tool.Chat},
		Approval:  &tool.Approval{Apply: trackPackage, Label: "Track package", Icon: "package", Module: module.Mail, Detail: "tracking_number"},
	})}
}

func trackPackage(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	id, _, err := tool.IntArg(args, "package_id", false)
	if err != nil {
		return nil, err
	}
	number, _, err := tool.TextArg(args, "tracking_number", false)
	if err != nil {
		return nil, err
	}
	carrier, _, err := tool.TextArg(args, "carrier", false)
	if err != nil {
		return nil, err
	}
	country, _, err := tool.TextArg(args, "destination_country", false)
	if err != nil {
		return nil, err
	}
	postcode, _, err := tool.TextArg(args, "destination_postcode", false)
	if err != nil {
		return nil, err
	}
	if id == 0 && number == "" {
		return nil, tool.Error("give package_id or tracking_number")
	}
	target, settings, err := tracking.Resolve(ctx, tc.DB, tc.Env, tc.Now(), id, number, carrier)
	if err != nil {
		return nil, trackingFailed(err)
	}
	if country != "" {
		settings.Country = country
	}
	if postcode != "" {
		settings.Postcode = postcode
	}
	if target.TrackerID != "" {
		result, _, err := tracking.Refresh(ctx, tc.DB, tc.Now(), target, settings, tc.Env)
		if err != nil {
			return nil, trackingFailed(err)
		}
		return result, nil
	}
	writeID, err := writes.Begin(ctx, tc.DB.Write, writes.New{
		TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "track_package",
		Target: fmt.Sprintf("package:%d", target.ID),
	})
	if err != nil {
		return nil, err
	}
	result, err := tracking.Register(ctx, tc.DB, tc.Now(), target, settings)
	if err != nil {
		ledger := context.WithoutCancel(ctx)
		if tracking.OutcomeUnknown(err) {
			_ = writes.SettleUnknown(ledger, tc.DB.Write, writeID, writes.New{Target: fmt.Sprintf("package:%d", target.ID)}, err.Error())
			return nil, tool.Error("the Ship24 registration outcome is unknown: " + err.Error() +
				"; the write is blocked until the operator resolves it with 'atrium writes resolve <id>'")
		}
		_ = writes.Discard(ledger, tc.DB.Write, writeID)
		return nil, trackingFailed(err)
	}
	if err := writes.Settle(context.WithoutCancel(ctx), tc.DB.Write, tc.Undoers, writeID, writes.New{
		Target: fmt.Sprintf("package:%d", target.ID),
		After:  map[string]any{"tracker_id": result.TrackerID, "status": result.Status},
		Undo:   tracking.Undo{Op: tracking.UndoUntrack, PackageID: target.ID, TrackerID: result.TrackerID, Before: target},
	}); err != nil {
		return nil, err
	}
	result.WriteID = writeID
	return result, nil
}

func trackingFailed(err error) error {
	var quota tracking.QuotaError
	var failure tracking.Error
	var missing config.NotConfigured
	var status *tracking.StatusError
	var limited *tracking.RateLimitError
	var transport *tracking.TransportError
	switch {
	case errors.As(err, &quota):
		return tool.Error(quota.Error())
	case errors.As(err, &failure):
		return tool.Error(failure.Error())
	case errors.As(err, &missing):
		return tool.NotConfigured(missing.Error())
	case errors.As(err, &status):
		return tool.Error(status.Error())
	case errors.As(err, &limited):
		return tool.Error(limited.Error())
	case errors.As(err, &transport):
		return tool.Error(transport.Error())
	}
	return err
}

func undoPackageUntrack(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u tracking.Undo
	if err := json.Unmarshal(raw, &u); err != nil || u.Op != tracking.UndoUntrack || u.PackageID == 0 {
		return nil, writes.UndoError("package undo payload is invalid")
	}
	result, err := tracking.Untrack(ctx, q, u)
	if err != nil {
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return nil, writes.UndoError(missing.Error())
		}
		return nil, writes.UndoError(err.Error())
	}
	return result, nil
}
