package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		switch {
		case r.URL.Path == "/v1/search":
			var body struct {
				Filter struct {
					Value string `json:"value"`
				} `json:"filter"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Filter.Value != "page" {
				_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false,"next_cursor":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":"list","results":[{"object":"page","id":"` + pageID + `","url":"https://notion.example/secret","last_edited_time":"2026-01-01T00:00:00.000Z","in_trash":false,"parent":{"type":"workspace"},"properties":{"title":{"type":"title","title":[{"plain_text":"Secret page"}]},"sensitivity":{"type":"formula","formula":{"type":"string","string":"private"}}}}],"has_more":false,"next_cursor":null}`))
		case r.URL.Path == "/v1/pages/"+pageID+"/markdown":
			_, _ = w.Write([]byte(`{"markdown":"secret alpha beta","truncated":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	f := newFixture(t, goldenData{}, map[string]string{"NOTION_TOKEN": "ntn_good"}, "")
	setEnv(t, "ATRIUM_NOTION_BASE_URL", srv.URL)
	ctx := context.Background()
	if code, _, stderr := f.Run(ctx, "notion", "sync"); code != 0 {
		t.Fatalf("notion sync exited %d: %s", code, stderr)
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
