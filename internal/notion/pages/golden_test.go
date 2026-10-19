package pages

import (
	"bytes"
	"html/template"
	"io/fs"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func goldenEngine(t testing.TB, c tpltest.Case) *tpl.Engine {
	t.Helper()
	globals := map[string]any{}
	for k, v := range c.Globals {
		globals[k] = v
	}
	version, _ := c.Globals["asset_version"].(string)
	e, err := tpl.New(tpl.Config{AssetVersion: version, Globals: globals, Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func goldenFrame(c tpltest.Case, page string, data any) *tpl.Frame {
	f := &tpl.Frame{Data: data, Page: page}
	f.CSRF, _ = c.Globals["csrf_token"].(string)
	f.Mode, _ = c.Globals["mode"].(string)
	f.Paused, _ = c.Context["paused"].(bool)
	if n, ok := c.Context["pending_count"].(int64); ok {
		f.PendingCount = int(n)
	}
	return f
}

func gmap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func gstr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func gopt(m map[string]any, key string) *string {
	if s, ok := m[key].(string); ok {
		return &s
	}
	return nil
}

func gnum(m map[string]any, key string) float64 {
	switch n := m[key].(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	}
	return 0
}

func gbool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

func gcrumbs(v any) []crumb {
	list, _ := v.([]any)
	out := make([]crumb, len(list))
	for i, e := range list {
		m := gmap(e)
		out[i] = crumb{ID: gopt(m, "id"), Title: gstr(m, "title")}
	}
	return out
}

func goldenPage(c tpltest.Case) pageView {
	m := gmap(c.Context["notion_page"])
	rows, _ := m["properties"].([]any)
	props := make([]property, len(rows))
	for i, e := range rows {
		row := gmap(e)
		props[i] = property{Name: gstr(row, "name"), Value: gstr(row, "value")}
	}
	body, _ := m["body_html"].(template.HTML)
	if text, ok := m["body_html"].(string); ok {
		body = template.HTML(text)
	}
	return pageView{
		ID: gstr(m, "id"), Title: gstr(m, "title"), URL: gstr(m, "url"), Path: gcrumbs(m["path"]), Private: gbool(m, "private"),
		InTrash: gbool(m, "in_trash"), EditedTS: gnum(m, "edited_ts"), Properties: props, BodyHTML: body,
	}
}

func goldenPageData(c tpltest.Case) any {
	list, _ := c.Context["related"].([]any)
	near := make([]related, len(list))
	for i, e := range list {
		m := gmap(e)
		near[i] = related{
			ID: gstr(m, "id"), Title: gstr(m, "title"), URL: gstr(m, "url"), Path: gcrumbs(m["path"]), Private: gbool(m, "private"),
			Linked: gbool(m, "linked"), Score: gnum(m, "score"),
		}
	}
	back := gmap(c.Context["back"])
	return pageData{NotionPage: goldenPage(c), Related: near, Back: backLink{URL: gstr(back, "url"), Label: gstr(back, "label")}}
}

func goldenArticle(c tpltest.Case) any { return articleData{NotionPage: goldenPage(c)} }

func goldenAction(c tpltest.Case) any {
	view := actionView{Error: gstr(c.Context, "error")}
	if m := gmap(c.Context["created"]); m != nil {
		id, _ := m["id"].(int64)
		view.Created = &created{ID: id, Kind: gstr(m, "kind"), Title: gstr(m, "title")}
	}
	return view
}

var pageGoldens = map[string]func(tpltest.Case) any{
	"notion_page": goldenPageData, "notion_page@empty": goldenPageData,
}

func TestGoldenPages(t *testing.T) {
	for name, data := range pageGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c, "chat", data(c))); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}

var fragmentGoldens = map[string]func(tpltest.Case) any{
	"_notion_page": goldenArticle, "_notion_page@bare": goldenArticle,
	"_notion_action": goldenAction, "_notion_action@error": goldenAction,
}

func TestGoldenFragments(t *testing.T) {
	for name, data := range fragmentGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Fragment(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c, "", data(c))); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}
