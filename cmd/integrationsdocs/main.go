// Command integrationsdocs checks docs/integrations.md against the
// production integration and OIDC preset registries. It needs no database or
// module settings, so CI can run it without bootstrapping the app.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/integrations"

	_ "github.com/jeiang/atrium/internal/assistant/assistantmodule"
	_ "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	_ "github.com/jeiang/atrium/internal/mail/mailmodule"
	_ "github.com/jeiang/atrium/internal/media"
	_ "github.com/jeiang/atrium/internal/notify"
	_ "github.com/jeiang/atrium/internal/notion/notionmodule"
)

func main() {
	path := flag.String("path", "docs/integrations.md", "support matrix path")
	flag.Parse()
	raw, err := os.ReadFile(*path)
	if err != nil {
		fail(err)
	}
	doc := string(raw)
	var missing []string
	for _, i := range integrations.All() {
		if !strings.Contains(doc, "| `"+i.Name+"` |") {
			missing = append(missing, i.Name)
		}
	}
	for _, p := range oidc.Presets() {
		if !strings.Contains(doc, "| `"+p.Name+"` |") {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) > 0 {
		fail(fmt.Errorf("%s is missing: %s", *path, strings.Join(missing, ", ")))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
