package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/py"
)

// field is one attribute of the argparse Namespace a leaf of pyApp prints.
type field struct {
	key  string
	repr func() string
}

func dest(name string) string { return strings.ReplaceAll(name, "-", "_") }

func optStr(a *Args, name string, o Opt) field {
	v := a.String(name, "", o)
	return field{dest(name), func() string {
		if !a.Has(name) {
			return "None"
		}
		return py.Repr(*v)
	}}
}

func optStrDef(a *Args, name, def string, o Opt) field {
	v := a.String(name, def, o)
	return field{dest(name), func() string { return py.Repr(*v) }}
}

func optInt(a *Args, name string, def int, o Opt) field {
	v := a.Int(name, def, o)
	return field{dest(name), func() string { return strconv.Itoa(*v) }}
}

func optIntNone(a *Args, name string, o Opt) field {
	v := a.Int(name, 0, o)
	return field{dest(name), func() string {
		if !a.Has(name) {
			return "None"
		}
		return strconv.Itoa(*v)
	}}
}

func flagField(a *Args, name string, o Opt) field {
	v := a.Bool(name, o)
	return field{dest(name), func() string {
		if *v {
			return "True"
		}
		return "False"
	}}
}

func listField(a *Args, name string, o Opt) field {
	v := a.Repeated(name, o)
	return field{dest(name), func() string {
		items := make([]string, len(*v))
		for i, s := range *v {
			items[i] = py.Repr(s)
		}
		return "[" + strings.Join(items, ", ") + "]"
	}}
}

func argField(a *Args, name string) field {
	v := a.Arg(name, Opt{})
	return field{name, func() string { return py.Repr(*v) }}
}

func intArgField(a *Args, name string) field {
	v := a.IntArg(name, Opt{})
	return field{name, func() string { return strconv.Itoa(*v) }}
}

// leaf prints "OK <vars(namespace)>" as the Python probe does.
func leaf(path []string, help string, decl func(a *Args) []field) *Command {
	return &Command{Name: path[len(path)-1], Help: help, Setup: func(a *Args) Handler {
		var fields []field
		if decl != nil {
			fields = decl(a)
		}
		return func(env *Env) int {
			items := []string{"'command': " + py.Repr(path[0])}
			for i := 1; i < len(path); i++ {
				items = append(items, py.Repr(dest(path[i-1])+"_command")+": "+py.Repr(path[i]))
			}
			for _, f := range fields {
				items = append(items, py.Repr(f.key)+": "+f.repr())
			}
			_, _ = fmt.Fprintf(env.Stdout, "OK {%s}\n", strings.Join(items, ", "))
			return ExitOK
		}
	}}
}

func group(name string, subs ...*Command) *Command {
	return &Command{Name: name, Commands: subs}
}

func scans(name string, subs ...string) *Command {
	g := group(name)
	for _, sub := range subs {
		g.Add(leaf([]string{name, sub}, "", nil))
	}
	return g
}

// pyApp mirrors cli.py's parser().
func pyApp() *App {
	app := New("atrium", nil)
	account := func(a *Args) []field {
		return []field{optStr(a, "account", Opt{Choices: []string{"gmail", "icloud"}})}
	}
	live := scans("live", "backlog", "rebaseline", "pause", "resume", "status", "deletions")
	live.Add(
		leaf([]string{"live", "actions"}, "", func(a *Args) []field {
			return []field{optInt(a, "limit", 20, Opt{}), optStr(a, "kind", Opt{Choices: []string{"file", "flag", "trash", "correct"}})}
		}),
		leaf([]string{"live", "undo"}, "", func(a *Args) []field { return []field{intArgField(a, "id")} }),
	)
	app.Add(
		leaf([]string{"sync"}, "", account),
		leaf([]string{"watch"}, "", account),
		group("rules",
			leaf([]string{"rules", "import"}, "", func(a *Args) []field { return []field{argField(a, "file")} }),
			leaf([]string{"rules", "export"}, "", nil)),
		leaf([]string{"shadow-report"}, "", func(a *Args) []field { return []field{optStr(a, "since", Opt{})} }),
		leaf([]string{"reindex"}, "", nil),
		scans("rebalance", "scan"),
		scans("deletions", "scan"),
		scans("gmail-labels", "scan"),
		live,
		group("proposals",
			leaf([]string{"proposals", "scan-all"}, "", nil),
			leaf([]string{"proposals", "list"}, "", func(a *Args) []field {
				return []field{
					optStr(a, "status", Opt{Choices: []string{"pending", "approved", "dismissed", "applied", "failed", "suppressed"}}),
					optStr(a, "kind", Opt{}),
				}
			}),
			leaf([]string{"proposals", "approve"}, "", func(a *Args) []field {
				return []field{intArgField(a, "id"), optStr(a, "selection", Opt{Help: "JSON selection document"})}
			}),
			leaf([]string{"proposals", "dismiss"}, "", func(a *Args) []field {
				return []field{intArgField(a, "id"), flagField(a, "forever", Opt{})}
			}),
			leaf([]string{"proposals", "undo"}, "", func(a *Args) []field { return []field{intArgField(a, "id")} })),
		group("notion",
			leaf([]string{"notion", "sync"}, "", nil),
			leaf([]string{"notion", "search"}, "", func(a *Args) []field {
				return []field{argField(a, "query"), flagField(a, "private", Opt{}), optInt(a, "limit", 10, Opt{})}
			}),
			leaf([]string{"notion", "wiki-plan"}, "draft one proposal that turns every database and page into the wiki page tree", nil)),
		group("agent",
			leaf([]string{"agent", "scheduler"}, "", nil),
			leaf([]string{"agent", "run"}, "", func(a *Args) []field {
				return []field{
					optIntNone(a, "thread", Opt{Help: "existing thread id; omitted creates a new thread of --kind"}),
					optStr(a, "text", Opt{Required: true}),
					optStrDef(a, "kind", "chat", Opt{Choices: []string{"chat", "cron", "nightly", "sub", "code"}}),
				}
			})),
		group("calendar",
			leaf([]string{"calendar", "candidates"}, "", nil),
			leaf([]string{"calendar", "google-auth"}, "", func(a *Args) []field { return []field{optInt(a, "port", 0, Opt{})} })),
		scans("loops", "refresh"),
		scans("watches", "refresh"),
		group("money",
			leaf([]string{"money", "refresh"}, "", func(a *Args) []field {
				return []field{optInt(a, "limit", 1000, Opt{Help: "messages to extract in this run"})}
			})),
		group("media",
			leaf([]string{"media", "poll"}, "poll EZTV for every channel now", nil),
			leaf([]string{"media", "sweep"}, "select files in new media torrents and remove filed torrents qBittorrent stopped", nil)),
		leaf([]string{"serve"}, "", func(a *Args) []field {
			return []field{
				optStrDef(a, "host", "127.0.0.1", Opt{}),
				optIntNone(a, "port", Opt{Required: true}),
				optStrDef(a, "auth", "proxy", Opt{Choices: []string{"proxy", "local"}}),
				listField(a, "allowed-host", Opt{Help: "accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)"}),
			}
		}),
		leaf([]string{"codemode-worker"}, "", nil),
	)
	return app
}

// TestMatchesArgparse compares complete output and exit codes with Python 3.14's argparse running cli.py's parser()
// (stdout and stderr captured, COLUMNS as given, "" unset).
func TestMatchesArgparse(t *testing.T) {
	tests := []struct {
		columns string
		args    []string
		code    int
		stdout  string
		stderr  string
	}{
		{columns: "", args: []string{}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: the following arguments are required: command\n"},
		{columns: "", args: []string{"bogus"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: argument command: invalid choice: 'bogus' (choose from 'sync', 'watch', 'rules', 'shadow-report', 'reindex', 'rebalance', 'deletions', 'gmail-labels', 'live', 'proposals', 'notion', 'agent', 'calendar', 'loops', 'watches', 'money', 'media', 'serve', 'codemode-worker')\n"},
		{columns: "", args: []string{"--bogus"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: the following arguments are required: command\n"},
		{columns: "", args: []string{"sync", "--account", "work"}, code: 2, stdout: "", stderr: "usage: atrium sync [-h] [--account {gmail,icloud}]\natrium sync: error: argument --account: invalid choice: 'work' (choose from 'gmail', 'icloud')\n"},
		{columns: "", args: []string{"sync", "extra"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: extra\n"},
		{columns: "", args: []string{"sync", "--acc", "gmail"}, code: 0, stdout: "OK {'command': 'sync', 'account': 'gmail'}\n", stderr: ""},
		{columns: "", args: []string{"sync", "--account"}, code: 2, stdout: "", stderr: "usage: atrium sync [-h] [--account {gmail,icloud}]\natrium sync: error: argument --account: expected one argument\n"},
		{columns: "", args: []string{"live"}, code: 2, stdout: "", stderr: "usage: atrium live [-h]\n                   {backlog,rebaseline,pause,resume,status,deletions,actions,undo} ...\natrium live: error: the following arguments are required: live_command\n"},
		{columns: "", args: []string{"live", "bogus"}, code: 2, stdout: "", stderr: "usage: atrium live [-h]\n                   {backlog,rebaseline,pause,resume,status,deletions,actions,undo} ...\natrium live: error: argument live_command: invalid choice: 'bogus' (choose from 'backlog', 'rebaseline', 'pause', 'resume', 'status', 'deletions', 'actions', 'undo')\n"},
		{columns: "", args: []string{"live", "actions", "--limit", "x"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --limit: invalid int value: 'x'\n"},
		{columns: "", args: []string{"live", "actions", "--kind", "zzz"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --kind: invalid choice: 'zzz' (choose from 'file', 'flag', 'trash', 'correct')\n"},
		{columns: "", args: []string{"live", "undo"}, code: 2, stdout: "", stderr: "usage: atrium live undo [-h] id\natrium live undo: error: the following arguments are required: id\n"},
		{columns: "", args: []string{"live", "undo", "x"}, code: 2, stdout: "", stderr: "usage: atrium live undo [-h] id\natrium live undo: error: argument id: invalid int value: 'x'\n"},
		{columns: "", args: []string{"live", "actions", "extra"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: extra\n"},
		{columns: "", args: []string{"proposals", "approve"}, code: 2, stdout: "", stderr: "usage: atrium proposals approve [-h] [--selection SELECTION] id\natrium proposals approve: error: the following arguments are required: id\n"},
		{columns: "", args: []string{"proposals", "list", "--status", "zz"}, code: 2, stdout: "", stderr: "usage: atrium proposals list [-h]\n                             [--status {pending,approved,dismissed,applied,failed,suppressed}]\n                             [--kind KIND]\natrium proposals list: error: argument --status: invalid choice: 'zz' (choose from 'pending', 'approved', 'dismissed', 'applied', 'failed', 'suppressed')\n"},
		{columns: "", args: []string{"serve"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: the following arguments are required: --port\n"},
		{columns: "", args: []string{"serve", "--port", "x"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: argument --port: invalid int value: 'x'\n"},
		{columns: "", args: []string{"serve", "--auth", "x", "--port", "1"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: argument --auth: invalid choice: 'x' (choose from 'proxy', 'local')\n"},
		{columns: "", args: []string{"live", "status", "--x"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: --x\n"},
		{columns: "", args: []string{"-h"}, code: 0, stdout: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\n\npositional arguments:\n  {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"live", "-h"}, code: 0, stdout: "usage: atrium live [-h]\n                   {backlog,rebaseline,pause,resume,status,deletions,actions,undo} ...\n\npositional arguments:\n  {backlog,rebaseline,pause,resume,status,deletions,actions,undo}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"live", "actions", "-h"}, code: 0, stdout: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\n\noptions:\n  -h, --help            show this help message and exit\n  --limit LIMIT\n  --kind {file,flag,trash,correct}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "approve", "-h"}, code: 0, stdout: "usage: atrium proposals approve [-h] [--selection SELECTION] id\n\npositional arguments:\n  id\n\noptions:\n  -h, --help            show this help message and exit\n  --selection SELECTION\n                        JSON selection document\n", stderr: ""},
		{columns: "", args: []string{"serve", "-h"}, code: 0, stdout: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\n\noptions:\n  -h, --help            show this help message and exit\n  --host HOST\n  --port PORT\n  --auth {proxy,local}\n  --allowed-host ALLOWED_HOST\n                        accepted Host header; repeatable (proxy mode requires\n                        one, or ATRIUM_PUBLIC_HOST)\n", stderr: ""},
		{columns: "", args: []string{"sync", "-h"}, code: 0, stdout: "usage: atrium sync [-h] [--account {gmail,icloud}]\n\noptions:\n  -h, --help            show this help message and exit\n  --account {gmail,icloud}\n", stderr: ""},
		{columns: "", args: []string{"rules", "import", "-h"}, code: 0, stdout: "usage: atrium rules import [-h] file\n\npositional arguments:\n  file\n\noptions:\n  -h, --help  show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"rules", "import"}, code: 2, stdout: "", stderr: "usage: atrium rules import [-h] file\natrium rules import: error: the following arguments are required: file\n"},
		{columns: "", args: []string{"rules", "import", "a", "b"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: b\n"},
		{columns: "", args: []string{"shadow-report", "--since"}, code: 2, stdout: "", stderr: "usage: atrium shadow-report [-h] [--since SINCE]\natrium shadow-report: error: argument --since: expected one argument\n"},
		{columns: "", args: []string{"live", "actions", "--limit=5", "--kind=move", "extra"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --kind: invalid choice: 'move' (choose from 'file', 'flag', 'trash', 'correct')\n"},
		{columns: "", args: []string{"live", "actions", "--l", "3"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'actions', 'limit': 3, 'kind': None}\n", stderr: ""},
		{columns: "", args: []string{"live", "actions", "--limit", "-3"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'actions', 'limit': -3, 'kind': None}\n", stderr: ""},
		{columns: "", args: []string{"live", "undo", "-3"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'undo', 'id': -3}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "dismiss", "1", "--for"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'dismiss', 'id': 1, 'forever': True}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "dismiss", "--forever"}, code: 2, stdout: "", stderr: "usage: atrium proposals dismiss [-h] [--forever] id\natrium proposals dismiss: error: the following arguments are required: id\n"},
		{columns: "", args: []string{"agent", "run"}, code: 2, stdout: "", stderr: "usage: atrium agent run [-h] [--thread THREAD] --text TEXT\n                        [--kind {chat,cron,nightly,sub,code}]\natrium agent run: error: the following arguments are required: --text\n"},
		{columns: "", args: []string{"agent", "run", "--text", "x", "--kind", "zz"}, code: 2, stdout: "", stderr: "usage: atrium agent run [-h] [--thread THREAD] --text TEXT\n                        [--kind {chat,cron,nightly,sub,code}]\natrium agent run: error: argument --kind: invalid choice: 'zz' (choose from 'chat', 'cron', 'nightly', 'sub', 'code')\n"},
		{columns: "", args: []string{"notion", "-h"}, code: 0, stdout: "usage: atrium notion [-h] {sync,search,wiki-plan} ...\n\npositional arguments:\n  {sync,search,wiki-plan}\n    wiki-plan           draft one proposal that turns every database and page\n                        into the wiki page tree\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"--version"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: the following arguments are required: command\n"},
		{columns: "", args: []string{"sync", "--", "x"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: -- x\n"},
		{columns: "", args: []string{"sync", "--account", "gmail", "--account", "icloud", "--bogus", "x"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: --bogus x\n"},
		{columns: "", args: []string{"live", "actions", "--limit", "1e3"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --limit: invalid int value: '1e3'\n"},
		{columns: "", args: []string{"sync"}, code: 0, stdout: "OK {'command': 'sync', 'account': None}\n", stderr: ""},
		{columns: "", args: []string{"watch", "--account", "icloud"}, code: 0, stdout: "OK {'command': 'watch', 'account': 'icloud'}\n", stderr: ""},
		{columns: "", args: []string{"sync", "--account=gmail"}, code: 0, stdout: "OK {'command': 'sync', 'account': 'gmail'}\n", stderr: ""},
		{columns: "", args: []string{"sync", "--account=bogus"}, code: 2, stdout: "", stderr: "usage: atrium sync [-h] [--account {gmail,icloud}]\natrium sync: error: argument --account: invalid choice: 'bogus' (choose from 'gmail', 'icloud')\n"},
		{columns: "", args: []string{"sync", "--acc=icloud"}, code: 0, stdout: "OK {'command': 'sync', 'account': 'icloud'}\n", stderr: ""},
		{columns: "", args: []string{"rules", "import", "f.json"}, code: 0, stdout: "OK {'command': 'rules', 'rules_command': 'import', 'file': 'f.json'}\n", stderr: ""},
		{columns: "", args: []string{"rules", "export"}, code: 0, stdout: "OK {'command': 'rules', 'rules_command': 'export'}\n", stderr: ""},
		{columns: "", args: []string{"shadow-report", "--since", "2026-01-01"}, code: 0, stdout: "OK {'command': 'shadow-report', 'since': '2026-01-01'}\n", stderr: ""},
		{columns: "", args: []string{"shadow-report"}, code: 0, stdout: "OK {'command': 'shadow-report', 'since': None}\n", stderr: ""},
		{columns: "", args: []string{"reindex"}, code: 0, stdout: "OK {'command': 'reindex'}\n", stderr: ""},
		{columns: "", args: []string{"rebalance", "scan"}, code: 0, stdout: "OK {'command': 'rebalance', 'rebalance_command': 'scan'}\n", stderr: ""},
		{columns: "", args: []string{"deletions", "scan"}, code: 0, stdout: "OK {'command': 'deletions', 'deletions_command': 'scan'}\n", stderr: ""},
		{columns: "", args: []string{"gmail-labels", "scan"}, code: 0, stdout: "OK {'command': 'gmail-labels', 'gmail_labels_command': 'scan'}\n", stderr: ""},
		{columns: "", args: []string{"live", "backlog"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'backlog'}\n", stderr: ""},
		{columns: "", args: []string{"live", "actions"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'actions', 'limit': 20, 'kind': None}\n", stderr: ""},
		{columns: "", args: []string{"live", "actions", "--kind", "flag", "--limit=-2"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'actions', 'limit': -2, 'kind': 'flag'}\n", stderr: ""},
		{columns: "", args: []string{"live", "actions", "--limit", "5", "--limit", "6"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'actions', 'limit': 6, 'kind': None}\n", stderr: ""},
		{columns: "", args: []string{"live", "undo", "7"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'undo', 'id': 7}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "scan-all"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'scan-all'}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "list"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'list', 'status': None, 'kind': None}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "list", "--kind", "x", "--status", "pending"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'list', 'status': 'pending', 'kind': 'x'}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "approve", "1", "--selection", "{}"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'approve', 'id': 1, 'selection': '{}'}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "approve", "--sel={\"a\":1}", "2"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'approve', 'id': 2, 'selection': '{\"a\":1}'}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "undo", "3"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'undo', 'id': 3}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "dismiss", "1", "--forever"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'dismiss', 'id': 1, 'forever': True}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "dismiss", "1"}, code: 0, stdout: "OK {'command': 'proposals', 'proposals_command': 'dismiss', 'id': 1, 'forever': False}\n", stderr: ""},
		{columns: "", args: []string{"notion", "sync"}, code: 0, stdout: "OK {'command': 'notion', 'notion_command': 'sync'}\n", stderr: ""},
		{columns: "", args: []string{"notion", "search", "q", "--private", "--limit", "3"}, code: 0, stdout: "OK {'command': 'notion', 'notion_command': 'search', 'query': 'q', 'private': True, 'limit': 3}\n", stderr: ""},
		{columns: "", args: []string{"notion", "search", "--limit", "3", "q"}, code: 0, stdout: "OK {'command': 'notion', 'notion_command': 'search', 'query': 'q', 'private': False, 'limit': 3}\n", stderr: ""},
		{columns: "", args: []string{"notion", "search", "--private"}, code: 2, stdout: "", stderr: "usage: atrium notion search [-h] [--private] [--limit LIMIT] query\natrium notion search: error: the following arguments are required: query\n"},
		{columns: "", args: []string{"notion", "search", "q", "--p"}, code: 0, stdout: "OK {'command': 'notion', 'notion_command': 'search', 'query': 'q', 'private': True, 'limit': 10}\n", stderr: ""},
		{columns: "", args: []string{"notion", "wiki-plan"}, code: 0, stdout: "OK {'command': 'notion', 'notion_command': 'wiki-plan'}\n", stderr: ""},
		{columns: "", args: []string{"agent", "scheduler"}, code: 0, stdout: "OK {'command': 'agent', 'agent_command': 'scheduler'}\n", stderr: ""},
		{columns: "", args: []string{"agent", "run", "--text", "hi", "--thread", "4"}, code: 0, stdout: "OK {'command': 'agent', 'agent_command': 'run', 'thread': 4, 'text': 'hi', 'kind': 'chat'}\n", stderr: ""},
		{columns: "", args: []string{"agent", "run", "--text", "hi", "--kind", "code"}, code: 0, stdout: "OK {'command': 'agent', 'agent_command': 'run', 'thread': None, 'text': 'hi', 'kind': 'code'}\n", stderr: ""},
		{columns: "", args: []string{"agent", "run", "--text"}, code: 2, stdout: "", stderr: "usage: atrium agent run [-h] [--thread THREAD] --text TEXT\n                        [--kind {chat,cron,nightly,sub,code}]\natrium agent run: error: argument --text: expected one argument\n"},
		{columns: "", args: []string{"agent", "run", "--t", "x"}, code: 2, stdout: "", stderr: "usage: atrium agent run [-h] [--thread THREAD] --text TEXT\n                        [--kind {chat,cron,nightly,sub,code}]\natrium agent run: error: ambiguous option: --t could match --thread, --text\n"},
		{columns: "", args: []string{"agent", "run", "--te", "x"}, code: 0, stdout: "OK {'command': 'agent', 'agent_command': 'run', 'thread': None, 'text': 'x', 'kind': 'chat'}\n", stderr: ""},
		{columns: "", args: []string{"calendar", "candidates"}, code: 0, stdout: "OK {'command': 'calendar', 'calendar_command': 'candidates'}\n", stderr: ""},
		{columns: "", args: []string{"calendar", "google-auth"}, code: 0, stdout: "OK {'command': 'calendar', 'calendar_command': 'google-auth', 'port': 0}\n", stderr: ""},
		{columns: "", args: []string{"calendar", "google-auth", "--port", "-1"}, code: 0, stdout: "OK {'command': 'calendar', 'calendar_command': 'google-auth', 'port': -1}\n", stderr: ""},
		{columns: "", args: []string{"loops", "refresh"}, code: 0, stdout: "OK {'command': 'loops', 'loops_command': 'refresh'}\n", stderr: ""},
		{columns: "", args: []string{"watches", "refresh"}, code: 0, stdout: "OK {'command': 'watches', 'watches_command': 'refresh'}\n", stderr: ""},
		{columns: "", args: []string{"money", "refresh"}, code: 0, stdout: "OK {'command': 'money', 'money_command': 'refresh', 'limit': 1000}\n", stderr: ""},
		{columns: "", args: []string{"money", "refresh", "--limit", "7"}, code: 0, stdout: "OK {'command': 'money', 'money_command': 'refresh', 'limit': 7}\n", stderr: ""},
		{columns: "", args: []string{"money", "refresh", "--limit", "x"}, code: 2, stdout: "", stderr: "usage: atrium money refresh [-h] [--limit LIMIT]\natrium money refresh: error: argument --limit: invalid int value: 'x'\n"},
		{columns: "", args: []string{"media", "poll"}, code: 0, stdout: "OK {'command': 'media', 'media_command': 'poll'}\n", stderr: ""},
		{columns: "", args: []string{"media", "sweep"}, code: 0, stdout: "OK {'command': 'media', 'media_command': 'sweep'}\n", stderr: ""},
		{columns: "", args: []string{"codemode-worker"}, code: 0, stdout: "OK {'command': 'codemode-worker'}\n", stderr: ""},
		{columns: "", args: []string{"reindex", "x"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: x\n"},
		{columns: "", args: []string{"serve", "--port", "8000"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 8000, 'auth': 'proxy', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--a", "x", "--port", "1"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: ambiguous option: --a could match --auth, --allowed-host\n"},
		{columns: "", args: []string{"serve", "--h"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: ambiguous option: --h could match --help, --host\n"},
		{columns: "", args: []string{"serve", "--ho", "h", "--port", "1"}, code: 0, stdout: "OK {'command': 'serve', 'host': 'h', 'port': 1, 'auth': 'proxy', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", "1", "--allowed-host", "a", "--allowed-host", "b"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 1, 'auth': 'proxy', 'allowed_host': ['a', 'b']}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", "1_0"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 10, 'auth': 'proxy', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", " 5"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 5, 'auth': 'proxy', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", "+3"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 3, 'auth': 'proxy', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", "1__0"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: argument --port: invalid int value: '1__0'\n"},
		{columns: "", args: []string{"serve", "--port", "_1"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: argument --port: invalid int value: '_1'\n"},
		{columns: "", args: []string{"serve", "--port", "٣"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 3, 'auth': 'proxy', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port=8080", "--host=::1"}, code: 0, stdout: "OK {'command': 'serve', 'host': '::1', 'port': 8080, 'auth': 'proxy', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", "5", "--al", "x"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 5, 'auth': 'proxy', 'allowed_host': ['x']}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", "5", "--auth", "local"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 5, 'auth': 'local', 'allowed_host': []}\n", stderr: ""},
		{columns: "", args: []string{"serve", "--port", "5", "--auth=proxy", "--allowed-host=a=b"}, code: 0, stdout: "OK {'command': 'serve', 'host': '127.0.0.1', 'port': 5, 'auth': 'proxy', 'allowed_host': ['a=b']}\n", stderr: ""},
		{columns: "", args: []string{"proposals", "dismiss", "1", "--forever=1"}, code: 2, stdout: "", stderr: "usage: atrium proposals dismiss [-h] [--forever] id\natrium proposals dismiss: error: argument --forever: ignored explicit argument '1'\n"},
		{columns: "", args: []string{"proposals", "dismiss", "1", "--for=x"}, code: 2, stdout: "", stderr: "usage: atrium proposals dismiss [-h] [--forever] id\natrium proposals dismiss: error: argument --forever: ignored explicit argument 'x'\n"},
		{columns: "", args: []string{"proposals", "dismiss", "1", "--forever="}, code: 2, stdout: "", stderr: "usage: atrium proposals dismiss [-h] [--forever] id\natrium proposals dismiss: error: argument --forever: ignored explicit argument ''\n"},
		{columns: "", args: []string{"live", "actions", "-hx"}, code: 0, stdout: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\n\noptions:\n  -h, --help            show this help message and exit\n  --limit LIMIT\n  --kind {file,flag,trash,correct}\n", stderr: ""},
		{columns: "", args: []string{"live", "actions", "-h=1"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument -h/--help: ignored explicit argument '1'\n"},
		{columns: "", args: []string{"live", "actions", "--help=1"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument -h/--help: ignored explicit argument '1'\n"},
		{columns: "", args: []string{"live", "actions", "--he"}, code: 0, stdout: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\n\noptions:\n  -h, --help            show this help message and exit\n  --limit LIMIT\n  --kind {file,flag,trash,correct}\n", stderr: ""},
		{columns: "", args: []string{"--he"}, code: 0, stdout: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\n\npositional arguments:\n  {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"--h"}, code: 0, stdout: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\n\npositional arguments:\n  {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"-h", "bogus"}, code: 0, stdout: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\n\npositional arguments:\n  {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"bogus", "-h"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: argument command: invalid choice: 'bogus' (choose from 'sync', 'watch', 'rules', 'shadow-report', 'reindex', 'rebalance', 'deletions', 'gmail-labels', 'live', 'proposals', 'notion', 'agent', 'calendar', 'loops', 'watches', 'money', 'media', 'serve', 'codemode-worker')\n"},
		{columns: "", args: []string{"live", "bogus", "-h"}, code: 2, stdout: "", stderr: "usage: atrium live [-h]\n                   {backlog,rebaseline,pause,resume,status,deletions,actions,undo} ...\natrium live: error: argument live_command: invalid choice: 'bogus' (choose from 'backlog', 'rebaseline', 'pause', 'resume', 'status', 'deletions', 'actions', 'undo')\n"},
		{columns: "", args: []string{"live", "status", "-h"}, code: 0, stdout: "usage: atrium live status [-h]\n\noptions:\n  -h, --help  show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"-x"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: the following arguments are required: command\n"},
		{columns: "", args: []string{"-"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: argument command: invalid choice: '-' (choose from 'sync', 'watch', 'rules', 'shadow-report', 'reindex', 'rebalance', 'deletions', 'gmail-labels', 'live', 'proposals', 'notion', 'agent', 'calendar', 'loops', 'watches', 'money', 'media', 'serve', 'codemode-worker')\n"},
		{columns: "", args: []string{"sync", "-"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: -\n"},
		{columns: "", args: []string{"sync", ""}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: \n"},
		{columns: "", args: []string{"sync", "--account", "gmail", "x", "y"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: x y\n"},
		{columns: "", args: []string{"--bogus", "sync"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: --bogus\n"},
		{columns: "", args: []string{"live", "--bogus", "status"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: --bogus\n"},
		{columns: "", args: []string{"live", "-x", "status"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: -x\n"},
		{columns: "", args: []string{"--", "sync"}, code: 0, stdout: "OK {'command': 'sync', 'account': None}\n", stderr: ""},
		{columns: "", args: []string{"sync", "--"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: --\n"},
		{columns: "", args: []string{"live", "undo", "--", "5"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'undo', 'id': 5}\n", stderr: ""},
		{columns: "", args: []string{"live", "undo", "--", "-5"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'undo', 'id': -5}\n", stderr: ""},
		{columns: "", args: []string{"live", "undo", "-x"}, code: 2, stdout: "", stderr: "usage: atrium live undo [-h] id\natrium live undo: error: the following arguments are required: id\n"},
		{columns: "", args: []string{"live", "undo", "-1.5"}, code: 2, stdout: "", stderr: "usage: atrium live undo [-h] id\natrium live undo: error: argument id: invalid int value: '-1.5'\n"},
		{columns: "", args: []string{"live", "undo", "-.5"}, code: 2, stdout: "", stderr: "usage: atrium live undo [-h] id\natrium live undo: error: argument id: invalid int value: '-.5'\n"},
		{columns: "", args: []string{"live", "undo", "5", "--"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'undo', 'id': 5}\n", stderr: ""},
		{columns: "", args: []string{"live", "undo", "--", "5", "6"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: 6\n"},
		{columns: "", args: []string{"rules", "import", "-"}, code: 0, stdout: "OK {'command': 'rules', 'rules_command': 'import', 'file': '-'}\n", stderr: ""},
		{columns: "", args: []string{"rules", "import", "a b"}, code: 0, stdout: "OK {'command': 'rules', 'rules_command': 'import', 'file': 'a b'}\n", stderr: ""},
		{columns: "", args: []string{"rules", "import", "-a b"}, code: 0, stdout: "OK {'command': 'rules', 'rules_command': 'import', 'file': '-a b'}\n", stderr: ""},
		{columns: "", args: []string{"rules", "import", "--", "--x"}, code: 0, stdout: "OK {'command': 'rules', 'rules_command': 'import', 'file': '--x'}\n", stderr: ""},
		{columns: "", args: []string{"rules", "import", "--x"}, code: 2, stdout: "", stderr: "usage: atrium rules import [-h] file\natrium rules import: error: the following arguments are required: file\n"},
		{columns: "", args: []string{"live", "actions", "--limit", "--", "5"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --limit: expected one argument\n"},
		{columns: "", args: []string{"live", "actions", "--", "--limit", "5"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: unrecognized arguments: -- --limit 5\n"},
		{columns: "", args: []string{"live", "undo", "１２"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'undo', 'id': 12}\n", stderr: ""},
		{columns: "", args: []string{"live", "actions", "--kind", "é"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --kind: invalid choice: 'é' (choose from 'file', 'flag', 'trash', 'correct')\n"},
		{columns: "", args: []string{"live", "actions", "--kind", "it's"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --kind: invalid choice: \"it's\" (choose from 'file', 'flag', 'trash', 'correct')\n"},
		{columns: "", args: []string{"live", "actions", "--kind", "a\nb"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --kind: invalid choice: 'a\\nb' (choose from 'file', 'flag', 'trash', 'correct')\n"},
		{columns: "", args: []string{"--", "--", "sync"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: argument command: invalid choice: '--' (choose from 'sync', 'watch', 'rules', 'shadow-report', 'reindex', 'rebalance', 'deletions', 'gmail-labels', 'live', 'proposals', 'notion', 'agent', 'calendar', 'loops', 'watches', 'money', 'media', 'serve', 'codemode-worker')\n"},
		{columns: "", args: []string{"live", "--", "undo", "5"}, code: 0, stdout: "OK {'command': 'live', 'live_command': 'undo', 'id': 5}\n", stderr: ""},
		{columns: "", args: []string{"--help"}, code: 0, stdout: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\n\npositional arguments:\n  {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"rules", "-h"}, code: 0, stdout: "usage: atrium rules [-h] {import,export} ...\n\npositional arguments:\n  {import,export}\n\noptions:\n  -h, --help       show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"rules", "export", "-h"}, code: 0, stdout: "usage: atrium rules export [-h]\n\noptions:\n  -h, --help  show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"proposals", "-h"}, code: 0, stdout: "usage: atrium proposals [-h] {scan-all,list,approve,dismiss,undo} ...\n\npositional arguments:\n  {scan-all,list,approve,dismiss,undo}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"proposals", "list", "-h"}, code: 0, stdout: "usage: atrium proposals list [-h]\n                             [--status {pending,approved,dismissed,applied,failed,suppressed}]\n                             [--kind KIND]\n\noptions:\n  -h, --help            show this help message and exit\n  --status {pending,approved,dismissed,applied,failed,suppressed}\n  --kind KIND\n", stderr: ""},
		{columns: "", args: []string{"proposals", "dismiss", "-h"}, code: 0, stdout: "usage: atrium proposals dismiss [-h] [--forever] id\n\npositional arguments:\n  id\n\noptions:\n  -h, --help  show this help message and exit\n  --forever\n", stderr: ""},
		{columns: "", args: []string{"notion", "search", "-h"}, code: 0, stdout: "usage: atrium notion search [-h] [--private] [--limit LIMIT] query\n\npositional arguments:\n  query\n\noptions:\n  -h, --help     show this help message and exit\n  --private\n  --limit LIMIT\n", stderr: ""},
		{columns: "", args: []string{"agent", "-h"}, code: 0, stdout: "usage: atrium agent [-h] {scheduler,run} ...\n\npositional arguments:\n  {scheduler,run}\n\noptions:\n  -h, --help       show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"agent", "run", "-h"}, code: 0, stdout: "usage: atrium agent run [-h] [--thread THREAD] --text TEXT\n                        [--kind {chat,cron,nightly,sub,code}]\n\noptions:\n  -h, --help            show this help message and exit\n  --thread THREAD       existing thread id; omitted creates a new thread of\n                        --kind\n  --text TEXT\n  --kind {chat,cron,nightly,sub,code}\n", stderr: ""},
		{columns: "", args: []string{"money", "refresh", "-h"}, code: 0, stdout: "usage: atrium money refresh [-h] [--limit LIMIT]\n\noptions:\n  -h, --help     show this help message and exit\n  --limit LIMIT  messages to extract in this run\n", stderr: ""},
		{columns: "", args: []string{"media", "-h"}, code: 0, stdout: "usage: atrium media [-h] {poll,sweep} ...\n\npositional arguments:\n  {poll,sweep}\n    poll        poll EZTV for every channel now\n    sweep       select files in new media torrents and remove filed torrents\n                qBittorrent stopped\n\noptions:\n  -h, --help    show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"calendar", "-h"}, code: 0, stdout: "usage: atrium calendar [-h] {candidates,google-auth} ...\n\npositional arguments:\n  {candidates,google-auth}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"calendar", "google-auth", "-h"}, code: 0, stdout: "usage: atrium calendar google-auth [-h] [--port PORT]\n\noptions:\n  -h, --help   show this help message and exit\n  --port PORT\n", stderr: ""},
		{columns: "", args: []string{"shadow-report", "-h"}, code: 0, stdout: "usage: atrium shadow-report [-h] [--since SINCE]\n\noptions:\n  -h, --help     show this help message and exit\n  --since SINCE\n", stderr: ""},
		{columns: "", args: []string{"watch", "-h"}, code: 0, stdout: "usage: atrium watch [-h] [--account {gmail,icloud}]\n\noptions:\n  -h, --help            show this help message and exit\n  --account {gmail,icloud}\n", stderr: ""},
		{columns: "", args: []string{"codemode-worker", "-h"}, code: 0, stdout: "usage: atrium codemode-worker [-h]\n\noptions:\n  -h, --help  show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"live", "undo", "-h"}, code: 0, stdout: "usage: atrium live undo [-h] id\n\npositional arguments:\n  id\n\noptions:\n  -h, --help  show this help message and exit\n", stderr: ""},
		{columns: "", args: []string{"live", "actions", "--limit", "x", "-h"}, code: 2, stdout: "", stderr: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\natrium live actions: error: argument --limit: invalid int value: 'x'\n"},
		{columns: "", args: []string{"live", "actions", "-h", "--limit", "x"}, code: 0, stdout: "usage: atrium live actions [-h] [--limit LIMIT]\n                           [--kind {file,flag,trash,correct}]\n\noptions:\n  -h, --help            show this help message and exit\n  --limit LIMIT\n  --kind {file,flag,trash,correct}\n", stderr: ""},
		{columns: "40", args: []string{"-h"}, code: 0, stdout: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\n\npositional arguments:\n  {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker}\n\noptions:\n  -h, --help      show this help\n                  message and exit\n", stderr: ""},
		{columns: "40", args: []string{"serve", "-h"}, code: 0, stdout: "usage: atrium serve [-h] [--host HOST]\n                    --port PORT\n                    [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\n\noptions:\n  -h, --help      show this help\n                  message and exit\n  --host HOST\n  --port PORT\n  --auth {proxy,local}\n  --allowed-host ALLOWED_HOST\n                  accepted Host\n                  header; repeatable\n                  (proxy mode requires\n                  one, or\n                  ATRIUM_PUBLIC_HOST)\n", stderr: ""},
		{columns: "40", args: []string{"notion", "-h"}, code: 0, stdout: "usage: atrium notion [-h]\n                     {sync,search,wiki-plan} ...\n\npositional arguments:\n  {sync,search,wiki-plan}\n    wiki-plan     draft one proposal\n                  that turns every\n                  database and page\n                  into the wiki page\n                  tree\n\noptions:\n  -h, --help      show this help\n                  message and exit\n", stderr: ""},
		{columns: "40", args: []string{"serve"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST]\n                    --port PORT\n                    [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: the following arguments are required: --port\n"},
		{columns: "40", args: []string{"agent", "run", "-h"}, code: 0, stdout: "usage: atrium agent run [-h]\n                        [--thread THREAD]\n                        --text TEXT\n                        [--kind {chat,cron,nightly,sub,code}]\n\noptions:\n  -h, --help      show this help\n                  message and exit\n  --thread THREAD\n                  existing thread id;\n                  omitted creates a\n                  new thread of --kind\n  --text TEXT\n  --kind {chat,cron,nightly,sub,code}\n", stderr: ""},
		{columns: "120", args: []string{"-h"}, code: 0, stdout: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\n\npositional arguments:\n  {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "120", args: []string{"serve", "-h"}, code: 0, stdout: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}] [--allowed-host ALLOWED_HOST]\n\noptions:\n  -h, --help            show this help message and exit\n  --host HOST\n  --port PORT\n  --auth {proxy,local}\n  --allowed-host ALLOWED_HOST\n                        accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)\n", stderr: ""},
		{columns: "120", args: []string{"live", "-h"}, code: 0, stdout: "usage: atrium live [-h] {backlog,rebaseline,pause,resume,status,deletions,actions,undo} ...\n\npositional arguments:\n  {backlog,rebaseline,pause,resume,status,deletions,actions,undo}\n\noptions:\n  -h, --help            show this help message and exit\n", stderr: ""},
		{columns: "120", args: []string{"bogus"}, code: 2, stdout: "", stderr: "usage: atrium [-h]\n              {sync,watch,rules,shadow-report,reindex,rebalance,deletions,gmail-labels,live,proposals,notion,agent,calendar,loops,watches,money,media,serve,codemode-worker} ...\natrium: error: argument command: invalid choice: 'bogus' (choose from 'sync', 'watch', 'rules', 'shadow-report', 'reindex', 'rebalance', 'deletions', 'gmail-labels', 'live', 'proposals', 'notion', 'agent', 'calendar', 'loops', 'watches', 'money', 'media', 'serve', 'codemode-worker')\n"},
		{columns: "30", args: []string{"media", "-h"}, code: 0, stdout: "usage: atrium media [-h]\n                    {poll,sweep} ...\n\npositional arguments:\n  {poll,sweep}\n    poll\n        poll EZTV for every\n        channel now\n    sweep\n        select files in new\n        media torrents and\n        remove filed\n        torrents qBittorrent\n        stopped\n\noptions:\n  -h, --help\n        show this help\n        message and exit\n", stderr: ""},
		{columns: "25", args: []string{"proposals", "list", "--status", "x"}, code: 2, stdout: "", stderr: "usage: atrium proposals list\n       [-h]\n       [--status {pending,approved,dismissed,applied,failed,suppressed}]\n       [--kind KIND]\natrium proposals list: error: argument --status: invalid choice: 'x' (choose from 'pending', 'approved', 'dismissed', 'applied', 'failed', 'suppressed')\n"},
		{columns: "200", args: []string{"media", "-h"}, code: 0, stdout: "usage: atrium media [-h] {poll,sweep} ...\n\npositional arguments:\n  {poll,sweep}\n    poll        poll EZTV for every channel now\n    sweep       select files in new media torrents and remove filed torrents qBittorrent stopped\n\noptions:\n  -h, --help    show this help message and exit\n", stderr: ""},
		{columns: "1", args: []string{"serve", "-h"}, code: 0, stdout: "usage: atrium serve\n       [-h]\n       [--host HOST]\n       --port PORT\n       [--auth {proxy,local}]\n       [--allowed-host ALLOWED_HOST]\n\noptions:\n  -h, --help\n    show this\n    help\n    message and\n    exit\n  --host HOST\n  --port PORT\n  --auth {proxy,local}\n  --allowed-host ALLOWED_HOST\n    accepted\n    Host\n    header;\n    repeatable\n    (proxy mode\n    requires\n    one, or ATR\n    IUM_PUBLIC_\n    HOST)\n", stderr: ""},
		{columns: "x", args: []string{"serve", "-h"}, code: 0, stdout: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\n\noptions:\n  -h, --help            show this help message and exit\n  --host HOST\n  --port PORT\n  --auth {proxy,local}\n  --allowed-host ALLOWED_HOST\n                        accepted Host header; repeatable (proxy mode requires\n                        one, or ATRIUM_PUBLIC_HOST)\n", stderr: ""},
		{columns: "-5", args: []string{"serve"}, code: 2, stdout: "", stderr: "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n                    [--allowed-host ALLOWED_HOST]\natrium serve: error: the following arguments are required: --port\n"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s%q", tt.columns, tt.args), func(t *testing.T) {
			t.Setenv("COLUMNS", tt.columns)
			var stdout, stderr bytes.Buffer
			code := pyApp().Run(context.Background(), tt.args, &stdout, &stderr)
			if code != tt.code || stdout.String() != tt.stdout || stderr.String() != tt.stderr {
				t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s\nwant exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String(), tt.code, tt.stdout, tt.stderr)
			}
		})
	}
}

func TestSetupRunsOnlyAfterASuccessfulParse(t *testing.T) {
	t.Setenv("COLUMNS", "")
	for _, tt := range []struct {
		args  []string
		setup int
	}{
		{[]string{"sync"}, 1},
		{[]string{"sync", "extra"}, 0},
		{[]string{"sync", "-h"}, 0},
		{[]string{"sync", "--account", "work"}, 0},
	} {
		calls := 0
		app := New("atrium", func(io.Writer) { calls++ })
		app.Add(leaf([]string{"sync"}, "", nil))
		app.Run(context.Background(), tt.args, io.Discard, io.Discard)
		if calls != tt.setup {
			t.Errorf("%q: setup ran %d times, want %d", tt.args, calls, tt.setup)
		}
	}
}

func TestUsageError(t *testing.T) {
	t.Setenv("COLUMNS", "")
	app := New("atrium", nil)
	app.Add(group("live", &Command{Name: "actions", Setup: func(a *Args) Handler {
		limit := a.Int("limit", 20, Opt{})
		return func(env *Env) int { return env.UsageError("bad limit %d", *limit) }
	}}))
	var stdout, stderr bytes.Buffer
	code := app.Run(context.Background(), []string{"live", "actions", "--limit", "3"}, &stdout, &stderr)
	want := "usage: atrium live actions [-h] [--limit LIMIT]\natrium live actions: error: bad limit 3\n"
	if code != ExitUsage || stdout.Len() != 0 || stderr.String() != want {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestDestAndMetavar(t *testing.T) {
	t.Setenv("COLUMNS", "")
	app := New("atrium", nil)
	app.Add(&Command{Name: "g", Dest: "which", Commands: []*Command{{Name: "x", Setup: func(a *Args) Handler {
		a.String("path", "", Opt{Metavar: "FILE", Help: "where"})
		a.Arg("n", Opt{Metavar: "N"})
		return func(*Env) int { return ExitOK }
	}}}})
	var stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"g", "y"}, io.Discard, &stderr); code != ExitUsage ||
		!strings.HasSuffix(stderr.String(), "atrium g: error: argument which: invalid choice: 'y' (choose from 'x')\n") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"g", "x"}, io.Discard, &stderr); code != ExitUsage ||
		stderr.String() != "usage: atrium g x [-h] [--path FILE] N\natrium g x: error: the following arguments are required: N\n" {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestVersion(t *testing.T) {
	app := pyApp()
	var stdout bytes.Buffer
	if code := app.Run(context.Background(), []string{"--version"}, &stdout, io.Discard); code != ExitUsage {
		t.Errorf("without Version: exit code = %d, want %d", code, ExitUsage)
	}
	app.Version = "1.2.3"
	for _, args := range [][]string{{"--version"}, {"--version", "sync"}} {
		stdout.Reset()
		if code := app.Run(context.Background(), args, &stdout, io.Discard); code != ExitOK || stdout.String() != "atrium 1.2.3\n" {
			t.Errorf("%q: exit code = %d, stdout = %q", args, code, stdout.String())
		}
	}
	for _, args := range [][]string{{"--vers"}, {"sync", "--version"}} {
		if code := app.Run(context.Background(), args, io.Discard, io.Discard); code != ExitUsage {
			t.Errorf("%q: exit code = %d, want %d", args, code, ExitUsage)
		}
	}
}

func TestHandlerEnv(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app := New("atrium", nil)
	app.Stdin = strings.NewReader("input")
	app.Add(&Command{Name: "wait", Setup: func(*Args) Handler {
		return func(env *Env) int {
			cancel()
			<-env.Ctx.Done()
			if !errors.Is(env.Ctx.Err(), context.Canceled) {
				return ExitUsage
			}
			if in, _ := io.ReadAll(env.Stdin); string(in) != "input" {
				return ExitError
			}
			return 7
		}
	}})
	if code := app.Run(ctx, []string{"wait"}, io.Discard, io.Discard); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

// TestSplitLines compares with re.sub(r'\s+', ' ', text, flags=re.ASCII).strip() and textwrap.wrap(text, width).
func TestSplitLines(t *testing.T) {
	for _, tt := range []struct {
		text  string
		width int
		want  []string
	}{
		{"accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)", 20, []string{"accepted Host", "header; repeatable", "(proxy mode requires", "one, or", "ATRIUM_PUBLIC_HOST)"}},
		{"a well-known self-contained long-winded explanation -- with dashes", 12, []string{"a well-known", "self-", "contained", "long-winded", "explanation", "-- with", "dashes"}},
		{"supercalifragilisticexpialidocious-word and averyveryverylongwordwithoutbreaks", 10, []string{"supercalif", "ragilistic", "expialidoc", "ious-word", "and averyv", "eryverylon", "gwordwitho", "utbreaks"}},
		{"x---y----z hello--world foo-bar-baz", 7, []string{"x---y", "----z", "hello--", "world", "foo-", "bar-baz"}},
		{"--kind is omitted, see --thread", 9, []string{"--kind is", "omitted,", "see", "--thread"}},
		{"tab\there  and\n\nnewline", 11, []string{"tab here", "and newline"}},
		{"ab-cd-ef-gh-ij-kl", 5, []string{"ab-", "cd-", "ef-", "gh-", "ij-kl"}},
		{"1-2-3-4-5 a1-b2-c3", 4, []string{"1-2-", "3-4-", "5 a1", "-b2-", "c3"}},
		{"non\u00a0breaking spaces here", 8, []string{"non\u00a0brea", "king", "spaces", "here"}},
	} {
		if got := splitLines(tt.text, tt.width); !slices.Equal(got, tt.want) {
			t.Errorf("splitLines(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
		}
	}
}
