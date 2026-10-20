package notify

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func goldenNotifications(c tpltest.Case) []Notification {
	var out []Notification
	for _, raw := range c.Context["notifications"].([]any) {
		m := raw.(map[string]any)
		n := Notification{Title: m["title"].(string), Body: m["body"].(string), Delivered: store.Flag(m["delivered"].(bool))}
		switch ts := m["ts"].(type) {
		case int64:
			n.TS = float64(ts)
		case float64:
			n.TS = ts
		}
		if v, ok := m["thread_link"].(string); ok {
			n.ThreadLink = &v
		}
		if v, ok := m["error"].(string); ok {
			n.Error = &v
		}
		out = append(out, n)
	}
	return out
}

func TestGoldenNotificationsPage(t *testing.T) {
	for _, name := range []string{"notifications", "notifications@empty"} {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			globals := map[string]any{}
			for k, v := range c.Globals {
				globals[k] = v
			}
			e, err := tpl.New(tpl.Config{AssetVersion: c.Globals["asset_version"].(string), Globals: globals, Extra: Templates()})
			if err != nil {
				t.Fatal(err)
			}
			f := &tpl.Frame{
				CSRF:         c.Globals["csrf_token"].(string),
				Mode:         c.Globals["mode"].(string),
				Paused:       c.Context["paused"].(bool),
				PendingCount: int(c.Context["pending_count"].(int64)),
				Page:         "settings",
				Data:         listing{goldenNotifications(c)},
			}
			var out bytes.Buffer
			if err := e.Page(&out, "notifications.gohtml", f); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}

func TestPageListsNotifications(t *testing.T) {
	d := dbtest.Schema(t)
	cfg := config.Config{Mode: "shadow", DBPath: t.TempDir() + "/atrium.db"}
	render, err := web.NewRenderer(cfg, d, tpl.Config{Extra: Templates()})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(web.Options{Cfg: cfg, DB: d, Auth: web.AuthLocal, AllowedHosts: []string{"testserver"}, Render: render, Modules: []web.Module{Module}})
	if err != nil {
		t.Fatal(err)
	}
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings/notifications", nil)
		req.Host = "testserver"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("GET = %d %s: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
		return rec.Body.String()
	}
	empty := get()
	if !strings.Contains(empty, "No notifications") {
		t.Errorf("empty page = %s", empty)
	}
	if !strings.Contains(empty, `class="-mb-2 flex flex-wrap gap-x-1 border-b text-sm"`) || strings.Contains(empty, "overflow-x-auto") {
		t.Errorf("notifications settings tabs do not wrap")
	}
	link := "3"
	if _, err := Send(t.Context(), d.Write, config.Map(nil), Message{Title: "Hello", Body: "World", ThreadLink: &link, ThreadID: 3, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	body := get()
	for _, want := range []string{"Hello", "World", `href="/chat/3"`, "Local only", "not configured (NTFY_URL and NTFY_TOPIC)"} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("page contains ZgotmplZ")
	}
}
