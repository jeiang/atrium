package mailtools

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/py"
)

const maxContacts = 10

var contactsSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},` +
	`"limit":{"type":"integer","minimum":1,"maximum":` + strconv.Itoa(maxContacts) + `}},` +
	`"required":["query"],"additionalProperties":false}`)

func contactTools() []tool.Tool {
	return []tool.Tool{{
		Name:        "contacts_lookup",
		Description: "Look up people in the operator's address book by name, email, phone or organisation.",
		InputSchema: contactsSchema,
		Handler:     contactsLookup,
		Effect:      tool.Read,
		Contexts:    tool.AllKinds,
	}}
}

func contactsLookup(_ context.Context, tc *tool.Context, args py.Dict) (any, error) {
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 5, maxContacts, 1)
	if err != nil {
		return nil, err
	}
	if tc.Cfg.ContactsDir == "" {
		return nil, tool.NotConfigured("ATRIUM_CONTACTS_DIR is not set")
	}
	found := contacts.Search(tc.Cfg.ContactsDir, query, int(limit))
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
