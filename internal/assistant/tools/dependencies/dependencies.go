package dependencytools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/application"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/tool"
)

const statusSchema = `{"type": "object", "properties": {}, "additionalProperties": false}`

const recheckSchema = `{"type": "object", "properties": {` +
	`"connection": {"type": "string", "description": "A connection id from dependencies_status, such as service/eztv."}}, ` +
	`"required": ["connection"], "additionalProperties": false}`

type Service func(context.Context, *tool.Context) (*connectionsapp.Service, error)

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: "dependencies_status",
			Description: "Read what atrium's apps need from outside services and whether they have it: each need with its connection, state, " +
				"failure class (auth, unreachable, config), the features it serves and its repair; each open repair with its severity and the link " +
				"where the operator fixes it; credential expiry dates. Secrets and provider error text are never included. Send the fix link; do not ask for a secret.",
			InputSchema: json.RawMessage(statusSchema),
			Handler:     status(svc),
			Effect:      tool.Read,
			Annotated:   true,
			Channel:     tool.ChannelNone,
			Contexts:    tool.AllKinds,
		},
		{
			Name: "dependency_recheck",
			Description: "Test one connection now through the operator's Test path and return its new state and failure class. Allowed once per " +
				"connection every 5 minutes, only for an enabled connection that an app which is on needs, and never for one that needs the operator " +
				"to sign in again. It cannot change a credential, a setting or whether the connection is on.",
			InputSchema: json.RawMessage(recheckSchema),
			Handler:     recheck(svc),
			Effect:      tool.Read,
			Annotated:   true,
			Channel:     tool.ChannelNone,
			Contexts:    tool.Kinds{tool.Chat, tool.Cron},
		},
	}
}

type connectionFacts struct {
	label      string
	expires    *time.Time
	graceUntil *time.Time
}

func facts(v connectionsapp.View) connectionFacts {
	out := connectionFacts{label: v.Label, expires: v.ExpiresAt, graceUntil: v.GraceUntil}
	for _, c := range v.Credentials {
		if c.ExpiresAt != nil && (out.expires == nil || c.ExpiresAt.Before(*out.expires)) {
			out.expires = c.ExpiresAt
		}
	}
	return out
}

func stamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func status(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
		s, err := svc(ctx, tc)
		if err != nil {
			return nil, err
		}
		views, err := s.List(ctx, connectionsapp.ListInput{})
		if err != nil {
			return nil, err
		}
		deps, err := s.Dependencies(ctx, connectionsapp.DependenciesInput{})
		if err != nil {
			return nil, err
		}
		byID := map[connectionsapp.ID]connectionsapp.View{}
		for _, v := range views.Data {
			byID[v.ID] = v
		}
		needs := []map[string]any{}
		for _, b := range deps.Data.Needs {
			item := map[string]any{
				"app": b.Module, "app_on": b.Active, "capability": b.Capability, "features": b.Features, "soft": b.Soft,
				"availability": b.Availability, "state": b.State, "class": connectionsapp.Class(b.State),
			}
			if b.Connection != "" {
				f := facts(byID[b.Connection])
				item["connection"], item["label"] = b.Connection, f.label
				item["expires_at"], item["away_until"] = stamp(f.expires), stamp(f.graceUntil)
			}
			if b.Repair != "" {
				item["repair"] = b.Repair
			}
			needs = append(needs, item)
		}
		repairs := []map[string]any{}
		for _, r := range deps.Data.Repairs {
			f := facts(byID[r.Connection])
			repairs = append(repairs, map[string]any{
				"id": r.ID, "connection": r.Connection, "label": r.Label, "class": r.Class, "state": r.State, "severity": r.Severity,
				"owner": r.Owner, "features": r.Features, "fix": r.Fix, "expires_at": stamp(f.expires),
			})
		}
		return map[string]any{"needs": needs, "repairs": repairs}, nil
	}
}

func recheck(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		id, ok, err := tool.TextArg(args, "connection", true)
		if err != nil || !ok || id == "" {
			return nil, tool.Error("connection is required")
		}
		s, err := svc(ctx, tc)
		if err != nil {
			return nil, err
		}
		view, err := s.Recheck(ctx, connectionsapp.RecheckInput{ID: connectionsapp.ID(id)})
		var app *application.Error
		switch {
		case errors.As(err, &app) && app.Code != application.Internal:
			return nil, tool.Error(app.Message)
		case err != nil:
			return nil, err
		}
		return map[string]any{"connection": view.ID, "state": view.State, "class": connectionsapp.Class(view.State)}, nil
	}
}
