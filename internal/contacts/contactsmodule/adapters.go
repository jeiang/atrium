package contactsmodule

import (
	"context"
	"encoding/json"
	"math"
	"strconv"

	contactsapp "github.com/jeiang/atrium/internal/contacts/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

const schemaMaxContacts = 100

var contactsSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},` +
	`"include":{"type":"array","items":{"enum":["emails","phones","dates"]},"uniqueItems":true},` +
	`"limit":{"type":"integer","minimum":1,"maximum":` + strconv.Itoa(schemaMaxContacts) + `}},` +
	`"required":["query"],"additionalProperties":false}`)

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
	in := contactsapp.LookupInput{}
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	in.Query = query
	if _, ok := args.Get("limit"); ok {
		limit, err := tool.BoundedInt(args, "limit", 0, math.MaxInt64, math.MinInt64)
		if err != nil {
			return nil, err
		}
		in.Limit = &limit
	}
	if in.Include, err = included(args); err != nil {
		return nil, err
	}
	read, err := contactsapp.New(tc.DB, tc.Env).Lookup(ctx, in)
	if err != nil {
		return nil, tool.Fail(err)
	}
	marks := tool.Marks(0)
	if read.Data.PII {
		marks = tool.PII
	}
	return tool.Marked{Value: read.Data.Value, Marks: marks}, nil
}

func included(args tool.Args) ([]string, error) {
	value, ok := args.Get("include")
	if !ok || value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, tool.Error("include must be a list of emails, phones or dates")
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, tool.Error("include must be a list of emails, phones or dates")
		}
		out = append(out, name)
	}
	return out, nil
}
