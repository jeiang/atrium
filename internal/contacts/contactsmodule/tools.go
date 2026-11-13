package contactsmodule

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/tool"
)

func Requirements() []module.Requirement {
	return []module.Requirement{module.HostSetting(contacts.DirKey)}
}

func ToolDefinitions() []module.ToolDefinition {
	return []module.ToolDefinition{Definition()}
}

func syncCommand(env config.Env) *cli.Command {
	return davmirror.SyncCommand("mirror the iCloud address books into the contacts directory (read-only)",
		module.Contacts+"/"+passrun.CardDAV, passrun.CardDAV, mirrorContacts(env))
}

func mirrorContacts(env config.Env) davmirror.Prepare {
	return func(ctx context.Context, d *db.DB) (func(context.Context) (davmirror.Stats, error), error) {
		set, err := calendar.LoadSettings(ctx, d.Read)
		if err != nil {
			return nil, err
		}
		src, err := contacts.Resolve(ctx, d.Read)
		if err != nil {
			return nil, err
		}
		if src.State == contacts.Off {
			return func(context.Context) (davmirror.Stats, error) { return davmirror.Stats{}, nil }, nil
		}
		if src.State == contacts.Unset {
			return nil, config.NotConfigured(contacts.DirKey + " is not set")
		}
		if err := calendar.MirrorConfigured(ctx, set, src.Dir); err != nil {
			return nil, err
		}
		return func(ctx context.Context) (davmirror.Stats, error) {
			return calendar.SyncContactsMirror(ctx, env, set, src.Dir)
		}, nil
	}
}

const schemaMaxContacts = 100

var contactsSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},` +
	`"include":{"type":"array","items":{"enum":["emails","phones","dates"]},"uniqueItems":true},` +
	`"limit":{"type":"integer","minimum":1,"maximum":` + strconv.Itoa(schemaMaxContacts) + `}},` +
	`"required":["query"],"additionalProperties":false}`)

var includeFields = []string{"emails", "phones", "dates"}

func Metadata() tool.Tool {
	return tool.Tool{
		Name:        "contacts_lookup",
		Description: "Look up people in the operator's address book by name, email, phone or organisation. Returns name and organisation; pass include for emails, phones or dates.",
		InputSchema: contactsSchema,
		Effect:      tool.Read,
		Annotated:   true,
		Marks:       tool.PII,
		Contexts:    tool.AllKinds,
	}
}

func Tool() tool.Tool {
	t := Metadata()
	t.Handler = contactsLookup
	return t
}

func Definition() module.ToolDefinition {
	return module.ToolDefinition{Tool: Tool()}
}

func Tools() []tool.Tool {
	return []tool.Tool{Tool()}
}

func contactsLookup(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	maxContacts, err := contacts.MaxResults(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 5, maxContacts, 1)
	if err != nil {
		return nil, err
	}
	include, err := included(args)
	if err != nil {
		return nil, err
	}
	src, err := contacts.Resolve(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if src.State != contacts.Ready {
		return nil, tool.NotConfigured(contacts.DirKey + " is not set")
	}
	found := contacts.Search(src.Dir, query, int(limit))
	results := make([]any, len(found))
	marks := tool.Marks(0)
	for i, c := range found {
		card := map[string]any{}
		if c.Name != "" {
			card["name"] = c.Name
		}
		if c.Org != "" {
			card["org"] = c.Org
		}
		if include["emails"] && len(c.Emails) > 0 {
			card["emails"] = c.Emails
			marks |= tool.PII
		}
		if include["phones"] && len(c.Phones) > 0 {
			card["phones"] = c.Phones
			marks |= tool.PII
		}
		if include["dates"] && len(c.Dates) > 0 {
			dates := make([]any, len(c.Dates))
			for j, d := range c.Dates {
				var year any
				if d.Year != nil {
					year = int64(*d.Year)
				}
				dates[j] = map[string]any{"kind": d.Kind, "label": d.Label, "month": int64(d.Month),
					"day": int64(d.Day), "year": year}
			}
			card["dates"] = dates
			marks |= tool.PII
		}
		results[i] = card
	}
	return tool.Marked{Value: map[string]any{"results": results}, Marks: marks}, nil
}

func included(args tool.Args) (map[string]bool, error) {
	value, ok := args.Get("include")
	if !ok || value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, tool.Error("include must be a list of emails, phones or dates")
	}
	out := map[string]bool{}
	for _, item := range list {
		name, ok := item.(string)
		if !ok || !slices.Contains(includeFields, name) {
			return nil, tool.Error("include must be a list of emails, phones or dates")
		}
		out[name] = true
	}
	return out, nil
}
