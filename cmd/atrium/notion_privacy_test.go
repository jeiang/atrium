package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotionSyncTreatsAFormulaPrivacyValueAsPrivate(t *testing.T) {
	t.Parallel()
	const pageID = "11111111-1111-1111-1111-111111111111"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer ntn_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid."}`))
			return
		}
		switch r.URL.Path {
		case "/v1/search":
			var body struct {
				Filter struct {
					Value string `json:"value"`
				} `json:"filter"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Filter.Value {
			case "data_source":
				_, _ = w.Write([]byte(`{"object":"list","results":[{"object":"data_source","id":"22222222-2222-2222-2222-222222222222","title":[{"plain_text":"Projects"}],"parent":{"type":"database_id","database_id":"33333333-3333-3333-3333-333333333333"},"database_parent":{"type":"workspace"},"properties":{"Name":{"id":"title","name":"Name","type":"title","title":{}},"Link":{"id":"link","name":"Link","type":"url","url":{}}}}],"has_more":false,"next_cursor":null}`))
			case "page":
				_, _ = w.Write([]byte(`{"object":"list","results":[{"object":"page","id":"` + pageID + `","url":"https://notion.example/secret","last_edited_time":"2026-01-01T00:00:00.000Z","in_trash":false,"parent":{"type":"workspace"},"properties":{"title":{"type":"title","title":[{"plain_text":"Secret page"}]},"sensitivity":{"type":"formula","formula":{"type":"string","string":"private"}}}}],"has_more":false,"next_cursor":null}`))
			default:
				_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false,"next_cursor":null}`))
			}
		case "/v1/pages/" + pageID + "/markdown":
			_, _ = w.Write([]byte(`{"markdown":"secret alpha beta","truncated":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	f := newFixture(t, goldenData{}, nil, "")
	setEnv(t, "ATRIUM_HOST_CONFIG", notionHostConfig(t, f.dir, srv.URL, "ntn_good"))
	ctx := context.Background()
	if code, _, stderr := f.Run(ctx, "notion", "sync"); code != 0 {
		t.Fatalf("notion sync exited %d: %s", code, stderr)
	}
	d := mustConnect(t, filepath.Join(f.dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var outcome, actor, source string
	if err := d.Read.QueryRow("SELECT outcome, actor, source FROM operation_audit WHERE operation = 'wiki.sync' ORDER BY id DESC LIMIT 1").Scan(&outcome, &actor, &source); err != nil {
		t.Fatal(err)
	}
	if outcome != "ok" || actor != "operator" || source != "cli" {
		t.Fatalf("wiki sync audit = %s %s/%s", outcome, actor, source)
	}
	if code, stdout, stderr := f.Run(ctx, "notion", "search", "alpha"); code != 0 {
		t.Fatalf("notion search exited %d: %s", code, stderr)
	} else if strings.Contains(stdout, "Secret page") {
		t.Fatalf("a formula-private page was returned by the public search: %q", stdout)
	}
	if code, stdout, stderr := f.Run(ctx, "notion", "search", "--private", "alpha"); code != 0 {
		t.Fatalf("notion search --private exited %d: %s", code, stderr)
	} else if !strings.Contains(stdout, "Secret page\thttps://notion.example/secret") {
		t.Fatalf("the private search did not return the formula-private page: %q", stdout)
	}
}
