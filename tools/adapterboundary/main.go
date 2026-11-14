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
	{pattern: "internal/kb/kbcmd/*"},
	{pattern: "internal/kb/export/job.go"},
	{pattern: "internal/notion/command.go"},
	{pattern: "internal/notion/pages/*"},
	{pattern: "internal/mail/pages/*"},
	{pattern: "internal/mail/reviewpages/*"},
	{pattern: "internal/mail/accountpages/*"},
	{pattern: "internal/mail/mailcmd/*"},
	{pattern: "internal/mail/run/command.go"},
	{pattern: "internal/proposals/proposalscmd/*"},
	{pattern: "internal/actionlink/handler.go"},
	{pattern: "internal/actionlink/pages/*"},
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
	{pattern: "internal/writes/writescmd/*"},
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
	"internal/textx":              true,
	"internal/timex":              true,
	"internal/jsonx":              true,
	"internal/numx":               true,
	"internal/urlx":               true,
	"internal/notionid":           true,
	"internal/assistant/registry": true,
}

func main() {
	edges, err := scan()
	if err != nil {
		fmt.Fprintln(os.Stderr, "adapterboundary:", err)
		os.Exit(1)
	}
	for _, e := range edges {
		fmt.Fprintln(os.Stderr, "adapter imports a forbidden package:", e)
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
		if !isAdapter(file, src) {
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
			if forbidden(imp) {
				edges = append(edges, file+"\t"+imp)
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
