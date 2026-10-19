package mailtools_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/tool"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
)

var wantNames = []string{
	"commitments", "contacts_lookup", "draft_email_create", "draft_email_update", "mail_read", "mail_search",
	"open_loops", "packages", "upcoming_birthdays",
}

func TestToolsArePythonTools(t *testing.T) {
	got := mailtools.Tools()
	var names []string
	for _, tl := range got {
		names = append(names, tl.Name)
		registrytest.Verify(t, tl)
	}
	slices.Sort(names)
	if !slices.Equal(names, wantNames) {
		t.Errorf("tools %v, want %v", names, wantNames)
	}
}

func TestMatrixPerRunKind(t *testing.T) {
	r := registry.New()
	if err := r.Add(mailtools.Tools()...); err != nil {
		t.Fatal(err)
	}
	for _, e := range registrytest.Matrix(t) {
		if !slices.Contains(wantNames, e.Name) {
			continue
		}
		for _, k := range tool.AllKinds {
			found, err := r.For(k)
			if err != nil {
				t.Fatal(err)
			}
			offered := slices.ContainsFunc(found, func(tl tool.Tool) bool { return tl.Name == e.Name })
			if offered != e.Contexts.Has(k) {
				t.Errorf("%s offered in %s: %v, matrix %v", e.Name, k, offered, e.Contexts.Has(k))
			}
		}
	}
}

func TestNoToolSendsOrWritesAMailbox(t *testing.T) {
	for _, e := range registrytest.Matrix(t) {
		if !slices.Contains(wantNames, e.Name) {
			continue
		}
		switch e.Effect {
		case tool.Read:
		case tool.Write:
			if !strings.HasPrefix(e.Name, "draft_email_") || !slices.Equal(e.Contexts, tool.Kinds{tool.Chat}) {
				t.Errorf("%s writes outside the draft tools", e.Name)
			}
		default:
			t.Errorf("%s has effect %s", e.Name, e.Effect)
		}
	}
	forbiddenImports := []string{
		"internal/mail/smtp", "internal/mail/imapclient", "internal/mail/mailops", "internal/mail/applier",
		"internal/mail/live", "internal/proposals", "internal/safety", "net/smtp",
	}
	forbiddenCalls := []string{"Send", "Append", "Discard", "ResolveUnknown", "Gated", "Approve", "ApproveAndApply"}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			for _, bad := range forbiddenImports {
				if strings.Contains(imp.Path.Value, bad+`"`) {
					t.Errorf("%s imports %s", name, imp.Path.Value)
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && slices.Contains(forbiddenCalls, sel.Sel.Name) {
				t.Errorf("%s uses %s", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
