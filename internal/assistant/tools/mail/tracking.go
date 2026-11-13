package mailtools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/config"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

var trackPackageSchema = json.RawMessage(`{"type":"object","properties":{"package_id":{"type":"integer"},"tracking_number":{"type":"string"},"carrier":{"type":"string"},"destination_country":{"type":"string"},"destination_postcode":{"type":"string"}},"additionalProperties":false}`)

func trackingTools(svc func(*tool.Context) *mailapp.Service) []tool.Tool {
	return []tool.Tool{approval.Gate(tool.Tool{
		Name: "track_package",
		Description: "Ask the operator to approve registering a package's tracking number with Ship24 (a paid lookup); nothing is sent until they approve the card. " +
			"Use the id from the packages tool, or give a tracking_number. Undoable.",
		InputSchema: trackPackageSchema,
		Effect:      tool.Propose, Undo: tracking.UndoUntrack,
		Annotated: true,
		Channel:   tool.ChannelOwn,
		Contexts:  tool.Kinds{tool.Chat},
		Approval: &tool.Approval{Apply: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
			return trackPackage(ctx, tc, args, svc)
		}, Label: "Track package", Icon: "package", Module: module.Mail, Detail: "tracking_number"},
	})}
}

func trackPackage(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
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
	done, err := svc(tc).ProposeTracking(ctx, mailapp.ProposeTrackingInput{ProposalID: tc.ProposalID, PackageID: id, Number: number, Carrier: carrier, Country: country, Postcode: postcode})
	if err != nil {
		if failure, ok := errors.AsType[*mailapp.TrackingFailure](err); ok {
			return nil, trackingFailed(failure.Err)
		}
		if e, ok := errors.AsType[*application.Error](err); ok {
			return nil, tool.Error(e.Message)
		}
		return nil, err
	}
	return done.Data.Tracking, nil
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
