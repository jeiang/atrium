package packagestools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	pkgapp "github.com/jeiang/atrium/internal/apps/packages/application"
	"github.com/jeiang/atrium/internal/apps/packages/tracking"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
)

var trackPackageSchema = json.RawMessage(`{"type":"object","properties":{"package_id":{"type":"integer"},"tracking_number":{"type":"string"},"carrier":{"type":"string"},"destination_country":{"type":"string"},"destination_postcode":{"type":"string"}},"additionalProperties":false}`)

var untrackPackageSchema = json.RawMessage(`{"type":"object","properties":{"package_id":{"type":"integer"}},"required":["package_id"],"additionalProperties":false}`)

func trackingTools(svc Service) []tool.Tool {
	return []tool.Tool{
		asstapp.ApprovalGate(tool.Tool{
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
			}, Label: "Track package", Icon: "package", Module: unit.Packages, Detail: "tracking_number"},
		}),
		asstapp.ApprovalGate(tool.Tool{
			Name: "untrack_package",
			Description: "Ask the operator to approve stopping Ship24 updates for a tracked package; nothing is sent until they approve the card. " +
				"Use the id from the packages tool. The package stays listed.",
			InputSchema: untrackPackageSchema,
			Effect:      tool.Propose,
			Annotated:   true,
			Channel:     tool.ChannelOwn,
			Contexts:    tool.Kinds{tool.Chat},
			Approval: &tool.Approval{Apply: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return untrackPackage(ctx, tc, args, svc)
			}, Label: "Stop tracking package", Icon: "package", Module: unit.Packages, Detail: "package_id"},
		}),
	}
}

func trackPackage(ctx context.Context, tc *tool.Context, args tool.Args, svc Service) (any, error) {
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
	done, err := svc(tc).Track(ctx, pkgapp.TrackInput{ProposalID: tc.ProposalID, PackageID: id, Number: number, Carrier: carrier, Country: country, Postcode: postcode})
	if err != nil {
		return nil, toolError(err)
	}
	return done.Data.Tracking, nil
}

func untrackPackage(ctx context.Context, tc *tool.Context, args tool.Args, svc Service) (any, error) {
	id, _, err := tool.IntArg(args, "package_id", true)
	if err != nil {
		return nil, err
	}
	done, err := svc(tc).Untrack(ctx, pkgapp.PackageInput{ID: id})
	if err != nil {
		return nil, toolError(err)
	}
	return done.Data, nil
}

func toolError(err error) error {
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return tool.NotConfigured(missing.Error())
	}
	if failure, ok := errors.AsType[*pkgapp.TrackingFailure](err); ok {
		err = pkgapp.TrackingError(failure.Err)
	}
	if e, ok := errors.AsType[*application.Error](err); ok {
		if e.Code == application.NotConfigured {
			return tool.NotConfigured(e.Message)
		}
		return tool.Error(e.Message)
	}
	return err
}
