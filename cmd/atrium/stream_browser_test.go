package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/web"
)

func openBrowserStream(t *testing.T, client *http.Client, base string, threadID, runID int64, headers ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/chat/%d/events?run=%d", base, threadID, runID), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestStreamCapsPerBrowser(t *testing.T) {
	s := newStreamSite(t)
	free := s.hold()
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("late")))
	threadID, runID := s.start("caps per browser")
	waitRunning(t, s.d, runID)
	laptop, phone := asstBrowse(t, s.base), asstBrowse(t, s.base)
	for i := range 4 {
		if resp := openBrowserStream(t, laptop.client, s.base, threadID, runID); resp.StatusCode != http.StatusOK {
			t.Fatalf("laptop stream %d: %d", i+1, resp.StatusCode)
		}
	}
	if resp := openBrowserStream(t, laptop.client, s.base, threadID, runID); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("fifth laptop stream: %d", resp.StatusCode)
	}
	if resp := openBrowserStream(t, phone.client, s.base, threadID, runID); resp.StatusCode != http.StatusOK {
		t.Fatalf("a second browser shared the first browser's slots: %d", resp.StatusCode)
	}
	free()
}

func TestStreamBrowserRouteAnswersTheContract(t *testing.T) {
	s := newStreamSite(t)
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("browser answer")))
	threadID, runID := s.start("browser contract")
	_, last := s.events(s.token, threadID, runID, "")
	browser := asstBrowse(t, s.base)
	if resp := openBrowserStream(t, browser.client, s.base, threadID, runID, "Last-Event-ID", last); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("browser resume at the terminal: %d", resp.StatusCode)
	}
	if code, _, _ := browser.get(fmt.Sprintf("/chat/%d/events?run=%d&after=%s", threadID, runID, last), false); code != http.StatusNoContent {
		t.Fatalf("browser after at the terminal: %d", code)
	}
	for _, path := range []string{
		fmt.Sprintf("/api/v1/threads/%d/runs/%d/events?after=x", threadID, runID),
		fmt.Sprintf("/api/v1/threads/%d/runs/%d/events?after=1.5", threadID, runID),
	} {
		expectAPI(t, s.do(http.MethodGet, path, s.token, nil), http.StatusBadRequest, "bad_request")
	}
	if code, body, _ := browser.get(fmt.Sprintf("/chat/%d/events?run=%d&after=x", threadID, runID), false); code != http.StatusBadRequest {
		t.Fatalf("browser malformed after: %d %s", code, body)
	}
}

func TestStreamSignOutClosesTheBrowserStream(t *testing.T) {
	s := newAPISite(t)
	d := asstDB(t, s.dir)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	s.api.SetOnRequest(func(anthropictest.Request) { <-release })
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("late")))
	made := s.do(http.MethodPost, "/api/v1/threads", s.token("owner"), map[string]any{"text": "sign out"})
	if made.status != http.StatusCreated {
		t.Fatalf("create: %d %s", made.status, made.body)
	}
	threadID, runID := number(t, made.json["thread"].(map[string]any)["id"]), number(t, made.json["run_id"])
	waitRunning(t, d, runID)
	browser := oidcBrowser(t, s.base, s.idp, "owner")
	resp := openBrowserStream(t, browser, s.base, threadID, runID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("browser stream: %d", resp.StatusCode)
	}
	lines := streamLines(resp.Body)
	awaitLine(t, lines, 5*time.Second, "event: run_start")
	u, _ := url.Parse(s.base)
	token := ""
	for _, c := range browser.Jar.Cookies(u) {
		if c.Name == web.CSRFCookie {
			token = c.Value
		}
	}
	req, err := http.NewRequest(http.MethodPost, s.base+"/logout", strings.NewReader(url.Values{"csrf_token": {token}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	out, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Body.Close()
	if out.StatusCode != http.StatusSeeOther && out.StatusCode != http.StatusFound {
		t.Fatalf("logout: %d", out.StatusCode)
	}
	deadline := time.After(20 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the browser stream stayed open after sign-out")
		}
	}
}
