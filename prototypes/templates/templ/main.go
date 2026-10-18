// PROTOTYPE (#75, throwaway): a-h/templ port. `go run ./templ` from prototypes/templates.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/a-h/templ"
)

func render(name string, c templ.Component) {
	ctx := context.WithValue(context.Background(), ctxKey{}, "csrf-fake")
	f, err := os.Create(filepath.Join("out", "templ-"+name+".html"))
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := c.Render(ctx, f); err != nil {
		panic(fmt.Errorf("%s: %w", name, err))
	}
	fmt.Println("wrote", name)
}

func main() {
	raw, err := os.ReadFile("data.json")
	if err != nil {
		panic(err)
	}
	var data struct {
		Thread    ThreadPageData    `json:"thread"`
		Proposals ProposalsPageData `json:"proposals"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		panic(err)
	}
	render("thread", ThreadPage(data.Thread))
	render("proposals", ProposalsPage(data.Proposals))
}
