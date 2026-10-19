// Package reviewpages is the review pages of the mail side: proposals, activity, drafts, open loops and
// watches, with their htmx fragments.
package reviewpages

import (
	"embed"
	"io/fs"
)

//go:embed templates/*.gohtml
var templates embed.FS

const (
	sourceMail   = "mail"
	categoryMail = "mail"
)

// Templates is the page templates of this package, for web.Page.Templates.
func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}
