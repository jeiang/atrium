package contactsmodule

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	contactsapp "github.com/jeiang/atrium/internal/contacts/application"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/web"
)

type LookupRequest struct {
	Query   string `query:"q" json:"-"`
	Include string `query:"include" json:"-"`
	Limit   *int64 `query:"limit" json:"-" api:"min=1,max=100"`
}

type ContactDate struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Month int64  `json:"month"`
	Day   int64  `json:"day"`
	Year  *int64 `json:"year" api:"nullable"`
}

type Contact struct {
	Name   string        `json:"name,omitempty"`
	Org    string        `json:"org,omitempty"`
	Emails []string      `json:"emails,omitempty"`
	Phones []string      `json:"phones,omitempty"`
	Dates  []ContactDate `json:"dates,omitempty"`
}

type Contacts struct {
	Results []Contact `json:"results"`
}

func contactRoutes(d *web.Deps) []application.Binding {
	defs := contactsapp.Describe()
	svc := func() *contactsapp.Service { return contactsapp.New(d.DB, d.Env) }
	return []application.Binding{
		api.Query(d, defs[0], application.APIShape{Area: "integrations", Errors: []application.Code{application.Invalid, application.NotConfigured}},
			"GET /contacts", func(ctx context.Context, in LookupRequest) (application.Read[Contacts], error) {
				lookup := contactsapp.LookupInput{Query: in.Query, Limit: in.Limit}
				for name := range strings.SplitSeq(in.Include, ",") {
					if name = strings.TrimSpace(name); name != "" {
						lookup.Include = append(lookup.Include, name)
					}
				}
				read, err := svc().Lookup(ctx, lookup)
				if err != nil {
					return application.Read[Contacts]{}, err
				}
				out := Contacts{Results: []Contact{}}
				raw, err := json.Marshal(read.Data.Value)
				if err == nil {
					err = json.Unmarshal(raw, &out)
				}
				return application.Read[Contacts]{Revision: read.Revision, Data: out}, err
			}),
		api.Command(d, defs[1], application.APIShape{Area: "integrations", Errors: []application.Code{application.NotConfigured, application.Unavailable, application.Conflict}},
			"POST /contacts/sync", func(ctx context.Context, in contactsapp.SyncInput) (application.Change[davmirror.Stats], error) {
				return svc().Sync(ctx, in)
			}),
	}
}
