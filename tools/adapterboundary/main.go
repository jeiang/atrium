package main

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	module   = "github.com/jeiang/atrium"
	internal = module + "/internal/"
)

type adapter struct {
	pattern string
	marker  string
}

var adapters = []adapter{
	{pattern: "internal/assistant/pages/*"},
	{pattern: "internal/assistant/tools/**"},
	{pattern: "internal/assistant/agentcmd/*"},
	{pattern: "internal/assistant/memory/memorycmd/*"},
	{pattern: "internal/assistant/webfetch/tool.go"},
	{pattern: "internal/assistant/subagents/*/*", marker: "tool.Tool{"},
	{pattern: "internal/kb/export/job.go"},
	{pattern: "internal/mail/pages/*"},
	{pattern: "internal/mail/reviewpages/*"},
	{pattern: "internal/mail/accountpages/*"},
	{pattern: "internal/mail/mailcmd/*"},
	{pattern: "internal/mail/run/command.go"},
	{pattern: "internal/proposals/proposalscmd/*"},
	{pattern: "internal/calendar/calendarcmd/*"},
	{pattern: "internal/contacts/contactsmodule/adapters.go"},
	{pattern: "internal/media/pages/*"},
	{pattern: "internal/media/mediacmd/*"},
	{pattern: "internal/notify/notifypages/*"},
	{pattern: "internal/notify/notifytools/*"},
	{pattern: "internal/connections/connweb/*"},
	{pattern: "internal/connections/connectionscmd/*"},
	{pattern: "internal/settings/settingscmd/*"},
	{pattern: "internal/settingspage/*"},
	{pattern: "internal/secrets/secretscmd/*"},
	{pattern: "internal/auth/bearercmd/*"},
	{pattern: "internal/web/api.go"},
	{pattern: "internal/web/auth_routes.go"},
	{pattern: "internal/web/auth_settings.go"},
	{pattern: "internal/db/dbcmd/*"},
	{pattern: "internal/backup/*", marker: "cli.Command{"},
	{pattern: "internal/ledger/ledgercmd/*"},
	{pattern: "internal/serverops/*", marker: "scheduler.Definition"},
}

var roots = map[string]string{
	"internal/assistant/tools/*/*module/register.go": "module-registration",
	"internal/web/server.go":                         "web-composition-root",
}

var allowed = map[string]bool{
	"internal/application":        true,
	"internal/cli":                true,
	"internal/web":                true,
	"internal/web/tpl":            true,
	"internal/tool":               true,
	"internal/logx":               true,
	"internal/module":             true,
	"internal/unit":               true,
	"internal/textx":              true,
	"internal/timex":              true,
	"internal/jsonx":              true,
	"internal/numx":               true,
	"internal/urlx":               true,
	"internal/assistant/registry": true,
	"internal/review":             true,
	"internal/review/hub":         true,
}

var platform = map[string]bool{
	"internal/module":                 true,
	"internal/unit":                   true,
	"internal/tool":                   true,
	"internal/ledger":                 true,
	"internal/proposals":              true,
	"internal/proposals/proposalscmd": true,
	"internal/embed":                  true,
	"internal/ops":                    true,
	"internal/ops/collectors":         true,
	"internal/scheduler":              true,
	"internal/review":                 true,
	"internal/review/application":     true,
	"internal/review/hub":             true,
	"internal/notify":                 true,
	"internal/web":                    true,
	"internal/web/tpl":                true,
	"internal/davmirror":              true,
}

const (
	dayContract  = "internal/day"
	calendarUnit = module + "/internal/calendar"
)

var units = []string{
	"internal/mail", "internal/media", "internal/calendar", "internal/contacts", "internal/kb", "internal/tasks",
	"internal/weather", "internal/grafana", "internal/serverops", "internal/assistant/tools",
}

var isolated = map[string]bool{
	"internal/contacts": true,
}

func main() {
	edges, err := scan()
	if err != nil {
		fmt.Fprintln(os.Stderr, "adapterboundary:", err)
		os.Exit(1)
	}
	for _, e := range edges {
		fmt.Fprintln(os.Stderr, e)
	}
	if len(edges) > 0 {
		os.Exit(1)
	}
}

func scan() ([]string, error) {
	var edges []string
	fset := token.NewFileSet()
	err := filepath.WalkDir("internal", func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		file = filepath.ToSlash(file)
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		platformFile := platform[path.Dir(file)] || strings.HasPrefix(file, dayContract+"/")
		adapterFile := isAdapter(file, src)
		dayReader := strings.HasPrefix(file, "internal/assistant/") && !strings.HasPrefix(file, "internal/assistant/tools/")
		own := unitOf(file)
		if !platformFile && !adapterFile && !dayReader && !isolated[own] {
			return nil
		}
		f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if adapterFile && forbidden(imp) {
				edges = append(edges, "adapter imports a forbidden package: "+file+"\t"+imp)
			}
			if platformFile && unitOf(imp) != "" {
				edges = append(edges, "platform package imports a unit: "+file+"\t"+imp)
			}
			if dayReader && (imp == calendarUnit || strings.HasPrefix(imp, calendarUnit+"/")) {
				edges = append(edges, "core assistant package imports calendar instead of reading the day contract: "+file+"\t"+imp)
			}
			if other := unitOf(imp); isolated[own] && other != "" && other != own {
				edges = append(edges, "unit imports another unit: "+file+"\t"+imp)
			}
		}
		return nil
	})
	slices.Sort(edges)
	return slices.Compact(edges), err
}

func isAdapter(file string, src []byte) bool {
	for pattern := range roots {
		if matches(pattern, file) {
			return false
		}
	}
	for _, a := range adapters {
		if matches(a.pattern, file) && (a.marker == "" || bytes.Contains(src, []byte(a.marker))) {
			return true
		}
	}
	return false
}

func unitOf(name string) string {
	rel := strings.TrimPrefix(name, module+"/")
	for _, unit := range units {
		if rel == unit || strings.HasPrefix(rel, unit+"/") {
			return unit
		}
	}
	return ""
}

func matches(pattern, file string) bool {
	if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
		return strings.HasPrefix(file, dir+"/")
	}
	ok, _ := path.Match(pattern, file)
	return ok
}

func forbidden(imp string) bool {
	if imp == "database/sql" {
		return true
	}
	rel, ok := strings.CutPrefix(imp, module+"/")
	if !ok || !strings.HasPrefix(imp, internal) {
		return false
	}
	return !allowed[rel] && path.Base(rel) != "application"
}
