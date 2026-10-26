package contactsmodule

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/py"
)

type Module struct{}

func (Module) Register(module.Context) module.Registration {
	return module.Registration{
		Name:     module.Contacts,
		Requires: []module.Requirement{module.Setting(contacts.DirKey)},
		Commands: []*cli.Command{{Name: "contacts", Commands: []*cli.Command{syncCommand()}}},
		Tools:    func(r *registry.Registry) error { return r.Add(Tools()...) },
	}
}

func syncCommand() *cli.Command {
	return davmirror.SyncCommand("mirror the iCloud address books into the contacts directory (read-only)", passrun.CardDAV,
		func(ctx context.Context, d *db.DB) (davmirror.Stats, error) {
			set, err := calendar.LoadSettings(ctx, d.Read)
			if err != nil {
				return davmirror.Stats{}, err
			}
			dir, err := contacts.Dir(ctx, d.Read)
			if err != nil {
				return davmirror.Stats{}, err
			}
			return calendar.SyncContactsMirror(ctx, set, dir)
		})
}

const schemaMaxContacts = 10

var contactsSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},` +
	`"limit":{"type":"integer","minimum":1,"maximum":` + strconv.Itoa(schemaMaxContacts) + `}},` +
	`"required":["query"],"additionalProperties":false}`)

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        "contacts_lookup",
		Description: "Look up people in the operator's address book by name, email, phone or organisation.",
		InputSchema: contactsSchema,
		Handler:     contactsLookup,
		Effect:      tool.Read,
		Contexts:    tool.AllKinds,
	}}
}

func contactsLookup(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	r := assistant.Read(ctx, tc.DB.Read)
	maxContacts := r.Int(assistant.KeyMailMaxContacts)
	if err := r.Err(); err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 5, int64(maxContacts), 1)
	if err != nil {
		return nil, err
	}
	dir, err := contacts.Dir(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, tool.NotConfigured("ATRIUM_CONTACTS_CONTACTS_DIR is not set")
	}
	found := contacts.Search(dir, query, int(limit))
	results := make([]any, len(found))
	for i, c := range found {
		card := py.Dict{}
		if c.Name != "" {
			card = append(card, py.Pair{Key: "name", Value: c.Name})
		}
		if len(c.Emails) > 0 {
			card = append(card, py.Pair{Key: "emails", Value: c.Emails})
		}
		if len(c.Phones) > 0 {
			card = append(card, py.Pair{Key: "phones", Value: c.Phones})
		}
		if c.Org != "" {
			card = append(card, py.Pair{Key: "org", Value: c.Org})
		}
		if len(c.Dates) > 0 {
			dates := make([]any, len(c.Dates))
			for j, d := range c.Dates {
				var year any
				if d.Year != nil {
					year = int64(*d.Year)
				}
				dates[j] = py.Dict{{Key: "kind", Value: d.Kind}, {Key: "label", Value: d.Label}, {Key: "month", Value: int64(d.Month)},
					{Key: "day", Value: int64(d.Day)}, {Key: "year", Value: year}}
			}
			card = append(card, py.Pair{Key: "dates", Value: dates})
		}
		results[i] = card
	}
	return py.Dict{{Key: "results", Value: results}}, nil
}
