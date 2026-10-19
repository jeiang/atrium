package pages

import (
	"bytes"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func goldenEngine(t testing.TB, c tpltest.Case, extra fs.FS) *tpl.Engine {
	t.Helper()
	globals := map[string]any{}
	for k, v := range c.Globals {
		globals[k] = v
	}
	version, _ := c.Globals["asset_version"].(string)
	e, err := tpl.New(tpl.Config{AssetVersion: version, Globals: globals, Modules: []fs.FS{Templates()}, Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func goldenFrame(c tpltest.Case, page string, data any) *tpl.Frame {
	f := &tpl.Frame{Data: data, Page: page}
	f.CSRF, _ = c.Globals["csrf_token"].(string)
	f.Mode, _ = c.Globals["mode"].(string)
	if v, ok := c.Context["mode"].(string); ok {
		f.Mode = v
	}
	f.Paused, _ = c.Context["paused"].(bool)
	if n, ok := c.Context["pending_count"].(int64); ok {
		f.PendingCount = int(n)
	}
	if m, ok := c.Context["toast"].(map[string]any); ok {
		f.Toast = &tpl.Toast{Category: m["category"].(string), Title: m["title"].(string), Description: m["description"].(string)}
	}
	return f
}

func goldenData(t testing.TB, c tpltest.Case) map[string]any {
	t.Helper()
	data := map[string]any{}
	for k, v := range c.Context {
		data[k] = v
	}
	if _, ok := data["spending"].(map[string]any); ok {
		data["spending"] = c.Ordered(t, "spending")
	}
	for _, key := range []string{"banks", "default_senders"} {
		if _, ok := data[key].(map[string]any); ok {
			data[key] = c.Ordered(t, key)
		}
	}
	if bills, ok := data["bills"].([]any); ok {
		for i, b := range bills {
			m := b.(map[string]any)
			if _, ok := m["typical"].(map[string]any); ok {
				m["typical"] = c.Ordered(t, "bills."+strconv.Itoa(i)+".typical")
			}
		}
	}
	return data
}

func TestGoldenPages(t *testing.T) {
	names := map[string]string{
		"money": "money", "money@empty": "money",
		"money_transactions": "money_transactions", "money_transactions@plain": "money_transactions", "money_transactions@empty": "money_transactions",
		"money_payees": "money_payees", "money_payees@one": "money_payees", "money_payees@empty": "money_payees",
		"money_bills": "money_bills", "money_bills@empty": "money_bills",
		"money_accounts": "money_accounts", "money_accounts@empty": "money_accounts",
	}
	for name, page := range names {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c, nil).Page(&out, page+".gohtml", goldenFrame(c, "money", goldenData(t, c))); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}

func TestGoldenMacros(t *testing.T) {
	c := tpltest.Load(t, "_money.context.json")
	var defs strings.Builder
	var calls []any
	var names []string
	csrf, _ := c.Globals["csrf_token"].(string)
	for i, call := range c.Calls {
		calls = append(calls, call.Args)
		arg := func(n int) string { return fmt.Sprintf("(index (index .Data.calls %d) %d)", i, n) }
		var body string
		switch call.Macro {
		case "money_nav":
			body = fmt.Sprintf(`{{template "money_nav" %s}}`, arg(0))
		case "amount":
			body = fmt.Sprintf(`{{template "money_value" (dict "value" %s "currency" %s)}}`, arg(0), arg(1))
		case "money_amount":
			body = fmt.Sprintf(`{{template "money_amount" %s}}`, arg(0))
		case "kind_badge":
			body = fmt.Sprintf(`{{template "money_kind_badge" %s}}`, arg(0))
		case "assign_form":
			back := `"overview"`
			if len(call.Args) > 2 {
				back = arg(2)
			}
			body = fmt.Sprintf(`{{template "money_assign_form" (dict "tx" %s "accounts" %s "back" %s "csrf" %q)}}`, arg(0), arg(1), back, csrf)
		default:
			t.Fatalf("no macro spec for %s", call.Macro)
		}
		name := fmt.Sprintf("_call_%d", i)
		names = append(names, name)
		fmt.Fprintf(&defs, "{{define %q}}\n<section data-golden-call=\"%d\" data-macro=%q>\n%s\n</section>{{end}}\n", name, i, call.Macro, body)
	}
	e := goldenEngine(t, c, fstest.MapFS{"_harness.gohtml": {Data: []byte(defs.String())}})
	var out bytes.Buffer
	f := goldenFrame(c, "", map[string]any{"calls": calls})
	for _, name := range names {
		if err := e.Fragment(&out, name, f); err != nil {
			t.Fatal(err)
		}
	}
	tpltest.Compare(t, tpltest.Want(t, "_money.html"), out.String())
}
