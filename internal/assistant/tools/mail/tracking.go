package mailtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/tracking"
)

var trackPackageSchema = json.RawMessage(`{"type":"object","properties":{"package_id":{"type":"integer"},"tracking_number":{"type":"string"},"carrier":{"type":"string"},"destination_country":{"type":"string"},"destination_postcode":{"type":"string"}},"additionalProperties":false}`)

func trackingTools() []tool.Tool {
	return []tool.Tool{{
		Name: "track_package",
		Description: "Register a package's tracking number with Ship24 and return its current status and recent events. " +
			"Use the id from the packages tool, or give a tracking_number. Undoable.",
		InputSchema: trackPackageSchema,
		Handler:     trackPackage,
		Effect:      tool.WriteUndo, Undo: tracking.UndoUntrack,
		Annotated: true,
		Flags:     tool.Flags{ExternalEffect: true},
		Contexts:  tool.Kinds{tool.Chat},
	}}
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
		_ = writes.Discard(ctx, tc.DB.Write, writeID)
		return nil, trackingFailed(err)
	}
	if err := writes.Settle(ctx, tc.DB.Write, tc.Undoers, writeID, writes.New{
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
	switch {
	case errors.As(err, &quota):
		return tool.Error(quota.Error())
	case errors.As(err, &failure):
		return tool.Error(failure.Error())
	case errors.As(err, &missing):
		return tool.NotConfigured(missing.Error())
	}
	return err
}

func undoPackageUntrack(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u tracking.Undo
	if err := json.Unmarshal(raw, &u); err != nil || u.Op != tracking.UndoUntrack || u.PackageID == 0 {
		return nil, writes.UndoError("package undo payload is invalid")
	}
	return tracking.Untrack(ctx, q, 0, u)
}
