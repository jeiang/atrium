package pages

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

var libRewrites = []tpltest.Rewrite{{
	From: "Movie%20(2024)/Movie%20(2024).mkv",
	To:   "Movie%20%282024%29/Movie%20%282024%29.mkv",
	Why:  "html/template percent-encodes parentheses in a URL attribute; links built by the app are already quoted, so real output is unchanged",
	Only: func(token string) bool {
		return strings.HasPrefix(token, "<a ") && strings.Contains(token, "vpn.example")
	},
}}

func libEntriesOf(v any) []LibraryEntry {
	out := []LibraryEntry{}
	for _, item := range chList(v) {
		e := chMap(item)
		out = append(out, LibraryEntry{Kind: chStr(e["kind"]), Dir: chStr(e["dir"]), Title: chStr(e["title"]), Year: chStr(e["year"])})
	}
	return out
}

func libNumberOf(v any) *int64 {
	if v == nil {
		return nil
	}
	n := chInt(v)
	return &n
}

func libGoldenMovie(m map[string]any) MoviePage {
	movie := chMap(m["movie"])
	view := MovieView{Dir: chStr(movie["dir"]), Title: chStr(movie["title"]), Year: chStr(movie["year"]), Files: []FileLink{}}
	for _, item := range chList(movie["files"]) {
		f := chMap(item)
		view.Files = append(view.Files, FileLink{Name: chStr(f["name"]), Size: chInt(f["size"]), Public: chStr(f["public"]), VPN: chStr(f["vpn"])})
	}
	return MoviePage{Movie: view, TTLHours: chInt(m["ttl_hours"])}
}

func libGoldenShow(m map[string]any) ShowPage {
	show := chMap(m["show"])
	view := ShowView{Dir: chStr(show["dir"]), Title: chStr(show["title"]), Year: chStr(show["year"]), Seasons: []SeasonView{}}
	for _, item := range chList(show["seasons"]) {
		s := chMap(item)
		season := SeasonView{Number: libNumberOf(s["number"]), Dir: chStr(s["dir"]), Episodes: []EpisodeView{}}
		for _, entry := range chList(s["episodes"]) {
			e := chMap(entry)
			season.Episodes = append(season.Episodes, EpisodeView{
				Rel: chStr(e["rel"]), Name: chStr(e["name"]), Size: chInt(e["size"]), Public: chStr(e["public"]), VPN: chStr(e["vpn"]),
				Episode: libNumberOf(e["episode"]),
			})
		}
		view.Seasons = append(view.Seasons, season)
	}
	return ShowPage{Show: view, TTLHours: chInt(m["ttl_hours"])}
}

func TestLibGoldenPages(t *testing.T) {
	cases := []struct {
		name, template string
		data           func(map[string]any) any
		rewrites       []tpltest.Rewrite
	}{
		{"media_library", "media_library.gohtml", libGoldenLibrary, nil},
		{"media_library@empty", "media_library.gohtml", libGoldenLibrary, nil},
		{"media_library@error", "media_library.gohtml", libGoldenLibrary, nil},
		{"media_movie", "media_movie.gohtml", func(m map[string]any) any { return libGoldenMovie(m) }, libRewrites},
		{"media_movie@empty", "media_movie.gohtml", func(m map[string]any) any { return libGoldenMovie(m) }, libRewrites},
		{"media_show", "media_show.gohtml", func(m map[string]any) any { return libGoldenShow(m) }, libRewrites},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tpltest.Load(t, tc.name+".context.json")
			f := goldenFrame(c)
			f.Page = "media_library"
			f.Data = tc.data(c.Context)
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, tc.template, f); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, tc.name+".html"), out.String(), tc.rewrites...)
		})
	}
}

func libGoldenLibrary(m map[string]any) any {
	return LibraryPage{Error: chStr(m["error"]), Movies: libEntriesOf(m["movies"]), Shows: libEntriesOf(m["shows"])}
}

func TestLibGoldenLinks(t *testing.T) {
	c := tpltest.Load(t, "_media_links.context.json")
	var defs strings.Builder
	var args []any
	var names []string
	for i, call := range c.Calls {
		args = append(args, call.Args[0])
		name := fmt.Sprintf("_call_%d", i)
		names = append(names, name)
		fmt.Fprintf(&defs, "{{define %q}}\n<section data-golden-call=\"%d\" data-macro=%q>\n{{template \"media_file_links\" (dict \"f\" (index .Data.calls %d) \"ttl_hours\" .Data.ttl_hours)}}\n</section>{{end}}\n", name, i, call.Macro, i)
	}
	globals := map[string]any{}
	for k, v := range c.Globals {
		globals[k] = v
	}
	version, _ := c.Globals["asset_version"].(string)
	engine, err := tpl.New(tpl.Config{
		AssetVersion: version, Globals: globals, Modules: []fs.FS{Templates()},
		Extra: fstest.MapFS{"_harness.gohtml": {Data: []byte(defs.String())}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := goldenFrame(c)
	f.Data = map[string]any{"calls": args, "ttl_hours": c.Context["ttl_hours"]}
	var out bytes.Buffer
	for _, name := range names {
		if err := engine.Fragment(&out, name, f); err != nil {
			t.Fatal(err)
		}
	}
	tpltest.Compare(t, tpltest.Want(t, "_media_links.html"), out.String(), libRewrites...)
}
