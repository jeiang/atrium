package main

import (
	"bufio"
	"bytes"
	"errors"
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
	baseline = "tools/adapterboundary/baseline.txt"
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
	{pattern: "internal/contacts/contactsmodule/tools.go"},
	{pattern: "internal/media/pages/*"},
	{pattern: "internal/media/mediacmd/*"},
	{pattern: "internal/notify/web.go"},
	{pattern: "internal/notify/api.go"},
	{pattern: "internal/notify/tool.go"},
	{pattern: "internal/connections/connweb/*"},
	{pattern: "internal/connections/cli.go"},
	{pattern: "internal/settings/settingscmd/*"},
	{pattern: "internal/settingspage/*"},
	{pattern: "internal/secrets/secretscmd/*"},
	{pattern: "internal/web/api.go"},
	{pattern: "internal/web/auth_routes.go"},
	{pattern: "internal/web/auth_settings.go"},
	{pattern: "internal/web/server.go"},
	{pattern: "internal/db/dbcmd/*"},
	{pattern: "internal/backup/*", marker: "cli.Command{"},
	{pattern: "internal/ledger/ledgercmd/*"},
	{pattern: "internal/writes/writescmd/*"},
	{pattern: "internal/assistant/tools/grafana/*"},
	{pattern: "internal/serverops/*", marker: "scheduler.Definition"},
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
	want, err := os.ReadFile(baseline)
	if errors.Is(err, fs.ErrNotExist) {
		for _, e := range edges {
			fmt.Println(e)
		}
		fmt.Fprintln(os.Stderr, "adapterboundary: "+baseline+" is missing; the edges above are the current baseline")
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "adapterboundary:", err)
		os.Exit(1)
	}
	listed := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(want))
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			listed[line] = true
		}
	}
	failed := false
	for _, e := range edges {
		if listed[e] {
			delete(listed, e)
			continue
		}
		fmt.Fprintln(os.Stderr, "adapter imports a forbidden package:", e)
		failed = true
	}
	stale := make([]string, 0, len(listed))
	for line := range listed {
		stale = append(stale, line)
	}
	slices.Sort(stale)
	for _, line := range stale {
		fmt.Fprintln(os.Stderr, "stale baseline line; delete it from "+baseline+":", line)
		failed = true
	}
	if failed {
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
