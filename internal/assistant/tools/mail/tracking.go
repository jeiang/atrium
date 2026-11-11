package mailtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
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
		Channel:   tool.ChannelOwn,
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
	identity := tracking.Identity(target.Carrier, target.TrackingNumber)
	ticket, err := tc.Ledger.Begin(ctx, ledger.New{
		Origin: "tracking", Operation: "track_package", OperationKey: "track_package:" + identity,
		Target: identity, ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
		Resources: []ledger.Resource{{Key: identity}},
	}, func(ctx context.Context, tx *sql.Tx) error {
		id, err := tracking.Admit(ctx, tx, target, tc.Now())
		if err != nil {
			return err
		}
		target.ID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	result, err := tracking.Register(ctx, tc.DB, tc.Now(), target, settings)
	if err != nil {
		settleCtx := context.WithoutCancel(ctx)
		state := ledger.Failed
		if tracking.OutcomeUnknown(err) {
			state = ledger.Unknown
		}
		_ = tc.Ledger.Settle(settleCtx, ticket, ledger.Result{State: state, Target: identity, Error: err.Error()}, nil)
		if state == ledger.Unknown {
			return nil, tool.Error("the Ship24 registration outcome is unknown: " + err.Error() +
				"; the write is blocked until the operator resolves it with `atrium effects resolve " + fmt.Sprint(ticket.ID) + " --outcome applied`")
		}
		return nil, trackingFailed(err)
	}
	undo := tracking.Undo{Op: tracking.UndoUntrack, PackageID: target.ID, TrackerID: result.TrackerID, Before: target}
	if err := tc.Ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: identity,
		After: jsonOf(map[string]any{"tracker_id": result.TrackerID, "status": result.Status}),
		Undo:  jsonOf(undo),
	}, func(ctx context.Context, tx *sql.Tx) error {
		return tracking.SaveRegistration(ctx, tx, tc.Now(), target.ID, result.TrackerID, result.Tracking)
	}); err != nil {
		return nil, err
	}
	result.WriteID = ticket.ID
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
