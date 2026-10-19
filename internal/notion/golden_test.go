package notion

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const (
	goldenToken = "golden-notion-token"
	pageA       = "00000000-0000-0000-0000-000000000001"
	pageB       = "00000000-0000-0000-0000-000000000004"
	database    = "00000000-0000-0000-0000-000000000201"
)

func fastClient(baseURL, token string, slept *[]float64) *Client {
	c := NewClient(token)
	c.BaseURL = baseURL
	c.Clock = func() float64 { return 0 }
	c.Sleep = func(_ context.Context, seconds float64) error {
		if slept != nil {
			*slept = append(*slept, seconds)
		}
		return nil
	}
	return c
}

func vector(index int) []float64 {
	v := make([]float64, config.EmbedDim)
	v[index] = 1
	return v
}

func topicEmbed(_ context.Context, _ string, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, text := range texts {
		index := 10
		lower := strings.ToLower(text)
		for j, topic := range []string{"alpha", "beta", "gamma"} {
			if strings.Contains(lower, topic) {
				index = j
				break
			}
		}
		out[i] = vector(index)
	}
	return out, nil
}

var _ embed.TextsFunc = topicEmbed

type commandRun struct {
	code           int
	stdout, stderr string
}

func runCommand(t *testing.T, dir string, env map[string]string, o Options, args ...string) commandRun {
	t.Helper()
	vars := map[string]string{
		"ATRIUM_DB":        filepath.Join(dir, "atrium.db"),
		"ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
	}
	for k, v := range env {
		vars[k] = v
	}
	o.Env = config.Map(vars)
	app := cli.New("atrium", nil)
	app.Add(Command(o))
	var out, errs bytes.Buffer
	code := app.Run(t.Context(), append([]string{"notion"}, args...), &out, &errs)
	return commandRun{code, out.String(), errs.String()}
}

func decodeOut(t *testing.T, raw string) py.Dict {
	t.Helper()
	v, err := py.JSONLoads([]byte(strings.TrimSpace(raw)))
	if err != nil {
		t.Fatalf("stdout %q: %v", raw, err)
	}
	return asDict(v)
}

func TestGoldenClientRequests(t *testing.T) {
	scenario := golden.LoadScenario(t, "notion", "client_requests")
	srv := golden.Replay(t, scenario.Exchanges)
	c := fastClient(srv.URL, goldenToken, nil)
	ctx := t.Context()
	discard := func(py.Dict) error { return nil }
	load := func(s string) any {
		v, err := py.JSONLoads([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	appendBody := asDict(load(string(scenario.Exchanges[12].Request.Body)))
	steps := []func() error{
		func() error { return c.Search(ctx, "page", false, discard) },
		func() error { return c.Search(ctx, "data_source", false, discard) },
		func() error { return c.Search(ctx, "page", true, discard) },
		func() error { _, err := c.GetPage(ctx, pageA); return err },
		func() error { _, err := c.PageMarkdown(ctx, pageA); return err },
		func() error {
			_, err := c.UpdateMarkdown(ctx, pageA, load(`{"type": "replace_content", "replace_content": {"new_str": "alpha roadmap v2"}}`))
			return err
		},
		func() error {
			_, err := c.UpdateMarkdown(ctx, pageA, load(`{"type": "update_content", "update_content": {"content_updates": [{"old_str": "v2", "new_str": "v3"}]}}`))
			return err
		},
		func() error {
			_, err := c.PatchProperties(ctx, pageA, load(`{"Type": {"id": "sel", "type": "select", "select": {"name": "Project"}}}`))
			return err
		},
		func() error {
			_, err := c.MovePage(ctx, pageB, load(`{"type": "page_id", "page_id": "`+pageA+`"}`))
			return err
		},
		func() error {
			_, err := c.CreatePage(ctx, load(`{"parent": {"page_id": "`+pageA+`"}, "properties": {"title": {"title": [{"type": "text", "text": {"content": "Child"}}]}}, "markdown": "# Child\n\nbody"}`))
			return err
		},
		func() error { _, err := c.SetTrash(ctx, pageB, true); return err },
		func() error { _, err := c.SetTrash(ctx, pageB, false); return err },
		func() error { _, err := c.AppendChildren(ctx, pageA, field(appendBody, "children")); return err },
		func() error { return c.BlockChildren(ctx, pageA, discard) },
		func() error { return c.PropertyItems(ctx, pageA, "a=b/c", discard) },
		func() error { _, err := c.SetDatabaseTrash(ctx, database, true); return err },
		func() error { _, err := c.SetDatabaseTrash(ctx, database, false); return err },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
}

func TestGoldenRetryAfterRateLimit(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "notion", "retry_rate_limited").Exchanges)
	var slept []float64
	c := fastClient(srv.URL, goldenToken, &slept)
	page, err := c.GetPage(t.Context(), pageA)
	if err != nil || text(page, "id") != pageA {
		t.Fatalf("GetPage = %v, %v", page, err)
	}
	if n := len(slices.DeleteFunc(slices.Clone(slept), func(s float64) bool { return s != 2 })); n != 2 {
		t.Errorf("retry waits = %v, want two of Retry-After's 2 seconds", slept)
	}
}

func TestGoldenSyncCommand(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "notion", "sync").Exchanges)
	run := runCommand(t, t.TempDir(), map[string]string{"NOTION_TOKEN": goldenToken}, Options{
		NewClient: func(token string) *Client { return fastClient(srv.URL, token, nil) },
		Embed:     topicEmbed,
	}, "sync")
	if run.code != cli.ExitOK || run.stderr != "" {
		t.Fatalf("exit %d, stderr %q", run.code, run.stderr)
	}
	out := decodeOut(t, run.stdout)
	want := map[string]int64{"sources": 1, "pages": 4, "new": 4, "updated": 0, "unchanged": 0, "trashed": 0, "failed": 0, "embedded": 4, "live": 4, "private": 1}
	for k, v := range want {
		if got, _ := field(out, k).(int64); got != v {
			t.Errorf("%s = %v, want %d", k, field(out, k), v)
		}
	}
	if keys := out; len(keys) != 11 || keys[0].Key != "sources" || keys[8].Key != "seconds" || keys[10].Key != "private" {
		t.Errorf("stdout keys = %v", keys)
	}
}

func TestGoldenSyncBlocksFallback(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "notion", "sync_blocks_fallback").Exchanges)
	dir := t.TempDir()
	run := runCommand(t, dir, map[string]string{"NOTION_TOKEN": goldenToken}, Options{
		NewClient: func(token string) *Client { return fastClient(srv.URL, token, nil) },
		Embed:     topicEmbed,
	}, "sync")
	if run.code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", run.code, run.stderr)
	}
	page := mirrored(t, dir, pageA)
	if want := "# Alpha\n- first\n  nested\n1. one"; page.Markdown != want {
		t.Errorf("markdown = %q, want %q", page.Markdown, want)
	}
}

func TestSyncWithoutTokenFailsLikePython(t *testing.T) {
	run := runCommand(t, t.TempDir(), nil, Options{}, "sync")
	if run.code != cli.ExitError || run.stdout != "" || run.stderr != "RuntimeError: NOTION_TOKEN is not set\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", run.code, run.stdout, run.stderr)
	}
}

func TestWikiPlanIsNotPorted(t *testing.T) {
	run := runCommand(t, t.TempDir(), nil, Options{}, "wiki-plan")
	if run.code != cli.ExitError || run.stderr != wikiPlanNotPorted+"\n" {
		t.Errorf("exit %d, stderr %q", run.code, run.stderr)
	}
}
