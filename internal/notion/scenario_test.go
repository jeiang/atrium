package notion

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/py"
)

var (
	a, b, c, d, e = uid(1), uid(2), uid(3), uid(4), uid(5)
)

func world(t *testing.T) (*fakeNotion, Options, map[string]string) {
	t.Helper()
	f, srv := newFake(t)
	f.addPage(a, "Alpha plan", "alpha roadmap and milestones")
	f.addPage(b, "Beta notes", "beta retrospective", parent("page_id", a))
	f.addPage(c, "Gamma ideas", "gamma brainstorm")
	return f, Options{
		NewClient: func(token string) *Client { return fastClient(srv.URL, token, nil) },
		Embed:     topicEmbed,
	}, map[string]string{"NOTION_TOKEN": "secret-token-value"}
}

func syncOK(t *testing.T, dir string, env map[string]string, o Options) map[string]int64 {
	t.Helper()
	run := runCommand(t, dir, env, o, "sync")
	if run.code != cli.ExitOK {
		t.Fatalf("sync: exit %d, stderr %q", run.code, run.stderr)
	}
	out := map[string]int64{}
	for _, p := range decodeOut(t, run.stdout) {
		if n, ok := p.Value.(int64); ok {
			out[p.Key] = n
		}
	}
	return out
}

func searchMirror(t *testing.T, dir, query string, private bool, embedFn func(context.Context, string, []string) ([][]float64, error)) Result {
	t.Helper()
	d := openMirror(t, dir)
	result, err := Search(t.Context(), d.Read, config.Config{}, query, private, 20, embedFn)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func down(context.Context, string, []string) ([][]float64, error) {
	return nil, &httpx.ServiceUnavailable{Msg: "offline"}
}

func ids(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	slices.Sort(out)
	return out
}

func TestSyncMirrorsPagesPathsAndIndex(t *testing.T) {
	f, o, env := world(t)
	f.addSource(uid(101), "Projects", "db-1")
	f.addPage(d, "Row one", "alpha row", parent("data_source_id", uid(101)))
	dir := t.TempDir()
	stats := syncOK(t, dir, env, o)
	if stats["pages"] != 4 || stats["new"] != 4 || stats["sources"] != 1 || stats["embedded"] != 4 || stats["live"] != 4 {
		t.Errorf("stats = %v", stats)
	}
	if got := mirrored(t, dir, b).Path; got != "Alpha plan" {
		t.Errorf("path of Beta notes = %q", got)
	}
	if got := mirrored(t, dir, d).Path; got != "Projects" {
		t.Errorf("path of Row one = %q", got)
	}
	if n := count(t, dir, "SELECT COUNT(*) FROM notion_fts"); n != 4 {
		t.Errorf("%d fts rows", n)
	}
	if n := count(t, dir, "SELECT COUNT(*) FROM notion_vec"); n != 4 {
		t.Errorf("%d vec rows", n)
	}
}

func TestIncrementalSyncRefetchesOnlyChanged(t *testing.T) {
	f, o, env := world(t)
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	before := f.markdownRequests()
	stats := syncOK(t, dir, env, o)
	if stats["unchanged"] != 3 || stats["updated"] != 0 || stats["new"] != 0 || f.markdownRequests() != before {
		t.Errorf("stats = %v, markdown requests %d -> %d", stats, before, f.markdownRequests())
	}
	f.setMarkdown(c, "gamma brainstorm revised")
	stats = syncOK(t, dir, env, o)
	if stats["unchanged"] != 2 || stats["updated"] != 1 {
		t.Errorf("stats = %v", stats)
	}
	if got := mirrored(t, dir, c).Markdown; !strings.Contains(got, "revised") {
		t.Errorf("markdown = %q", got)
	}
	if hits := searchMirror(t, dir, "revised", false, down).Hits; len(hits) == 0 || hits[0].ID != c {
		t.Errorf("hits = %v", hits)
	}
}

func TestTrashedAndDeletedPagesLeaveTheIndex(t *testing.T) {
	f, o, env := world(t)
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	f.setTrash(c, true)
	f.remove(b)
	if stats := syncOK(t, dir, env, o); stats["trashed"] != 2 {
		t.Errorf("stats = %v", stats)
	}
	if mirrored(t, dir, c).InTrash != 1 {
		t.Error("Gamma ideas is not trashed")
	}
	if n := count(t, dir, "SELECT COUNT(*) FROM notion_fts"); n != 1 {
		t.Errorf("%d fts rows", n)
	}
	if n := count(t, dir, "SELECT COUNT(*) FROM notion_vec"); n != 1 {
		t.Errorf("%d vec rows", n)
	}
	if hits := searchMirror(t, dir, "gamma", false, down).Hits; len(hits) != 0 {
		t.Errorf("hits = %v", hits)
	}
	f.setTrash(c, false)
	f.touch(c)
	syncOK(t, dir, env, o)
	if got := ids(searchMirror(t, dir, "gamma", false, down).Hits); !slices.Equal(got, []string{c}) {
		t.Errorf("hits = %v", got)
	}
}

func TestMarkdownFallsBackToBlocks(t *testing.T) {
	f, o, env := world(t)
	f.markdownStatus = 404
	f.blocks[a] = []py.Dict{
		{{Key: "id", Value: "b1"}, {Key: "type", Value: "heading_1"}, {Key: "heading_1", Value: py.Dict{{Key: "rich_text", Value: richJSON("Title")}}}, {Key: "has_children", Value: false}},
		{{Key: "id", Value: "b2"}, {Key: "type", Value: "paragraph"}, {Key: "paragraph", Value: py.Dict{{Key: "rich_text", Value: richJSON("alpha body")}}}, {Key: "has_children", Value: true}},
	}
	f.blocks["b2"] = []py.Dict{
		{{Key: "id", Value: "b3"}, {Key: "type", Value: "bulleted_list_item"}, {Key: "bulleted_list_item", Value: py.Dict{{Key: "rich_text", Value: richJSON("nested")}}}, {Key: "has_children", Value: false}},
	}
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	if got, want := mirrored(t, dir, a).Markdown, "# Title\nalpha body\n  - nested"; got != want {
		t.Errorf("markdown = %q, want %q", got, want)
	}
}

func TestPrivatePagesHiddenByDefaultAndInherited(t *testing.T) {
	f, o, env := world(t)
	f.addPage(d, "Secret alpha", "alpha secret", withProp("Sensitivity", selectProp("Private")))
	f.addPage(e, "Child alpha", "alpha child", parent("page_id", d))
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	if mirrored(t, dir, e).Private != 1 {
		t.Error("the child of a private page is not private")
	}
	for _, id := range ids(searchMirror(t, dir, "alpha", false, topicEmbed).Hits) {
		if id == d || id == e {
			t.Errorf("private page %s in the default hits", id)
		}
	}
	shown := ids(searchMirror(t, dir, "alpha", true, topicEmbed).Hits)
	if !slices.Contains(shown, d) || !slices.Contains(shown, e) {
		t.Errorf("private hits = %v", shown)
	}
	if got := ids(searchMirror(t, dir, "alpha", false, down).Hits); !slices.Equal(got, []string{a, b}) {
		t.Errorf("lexical default hits = %v", got)
	}
	for _, h := range searchMirror(t, dir, "", false, topicEmbed).Hits {
		if h.ID == d || h.ID == e {
			t.Errorf("private page %s in the recent list", h.ID)
		}
	}
}

func TestSearchDegradesWithoutEmbeddings(t *testing.T) {
	_, o, env := world(t)
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	result := searchMirror(t, dir, "retrospective", false, down)
	if result.Semantic || result.Error == nil || *result.Error != "offline" || len(result.Hits) == 0 {
		t.Fatalf("result = %+v", result)
	}
	hit := result.Hits[0]
	if hit.ID != b || hit.Title != "Beta notes" || !strings.HasSuffix(hit.URL, b) || len(hit.Path) != 1 || hit.Path[0].Title != "Alpha plan" {
		t.Errorf("hit = %+v", hit)
	}
	matched := false
	for _, p := range hit.SnippetParts {
		matched = matched || p.Highlighted
	}
	if !matched {
		t.Errorf("snippet parts = %v", hit.SnippetParts)
	}
}

func TestSensitivityFlipReindexesVisibility(t *testing.T) {
	f, o, env := world(t)
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	f.pages[c] = f.pages[c].Set("properties", asDict(field(f.pages[c], "properties")).Set("Sensitivity", selectProp("Private")))
	f.touch(c)
	syncOK(t, dir, env, o)
	var visibility string
	err := openMirror(t, dir).Read.QueryRowContext(t.Context(),
		"SELECT visibility FROM notion_vec WHERE rowid = (SELECT rid FROM notion_pages WHERE id = ?)", c).Scan(&visibility)
	if err != nil || visibility != "private" {
		t.Errorf("visibility = %q, %v", visibility, err)
	}
}

func TestRelatedLinksThenVectorsAndPrivacy(t *testing.T) {
	f, o, env := world(t)
	f.addPage(d, "Second alpha", "alpha again")
	f.addPage(e, "Hidden alpha", "alpha hidden", withProp("Sensitivity", selectProp("Private")))
	f.setMarkdown(c, "gamma mentions https://notion.invalid/"+strings.ReplaceAll(a, "-", ""))
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	mirror := openMirror(t, dir)
	rel, err := RelatedPages(t.Context(), mirror.Read, a, RelatedLimit)
	if err != nil || len(rel) == 0 || rel[0].ID != c || !rel[0].Linked {
		t.Fatalf("related = %+v, %v", rel, err)
	}
	var got []string
	for _, r := range rel {
		got = append(got, r.ID)
	}
	if !slices.Contains(got, d) || slices.Contains(got, e) || slices.Contains(got, a) {
		t.Errorf("related ids = %v", got)
	}
	hidden, err := RelatedPages(t.Context(), mirror.Read, e, RelatedLimit)
	if err != nil || len(hidden) == 0 {
		t.Fatalf("related of the private page = %+v, %v", hidden, err)
	}
	var fromHidden []string
	for _, r := range hidden {
		fromHidden = append(fromHidden, r.ID)
	}
	if !slices.Contains(fromHidden, a) {
		t.Errorf("related of the private page = %v", fromHidden)
	}
}

func TestSearchCommandPrintsTitleAndURL(t *testing.T) {
	_, o, env := world(t)
	dir := t.TempDir()
	syncOK(t, dir, env, o)
	o.Embed = down
	run := runCommand(t, dir, nil, o, "search", "retrospective", "--limit", "3")
	if want := "Beta notes\thttps://notion.invalid/" + b + "\n"; run.code != cli.ExitOK || run.stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want %q", run.code, run.stdout, run.stderr, want)
	}
}
