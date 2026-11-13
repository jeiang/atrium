package contactsmodule

import (
	"github.com/jeiang/atrium/internal/application"
	contactsapp "github.com/jeiang/atrium/internal/contacts/application"
)

func contactsOperations() ([]application.Definition, []application.Binding) {
	return contactsapp.Describe(), []application.Binding{
		{Kind: application.Tool, Target: "contacts_lookup", Uses: []application.Name{contactsapp.LookupName}},
		{Kind: application.CLI, Target: "contacts sync", Uses: []application.Name{contactsapp.SyncName}},
	}
}
