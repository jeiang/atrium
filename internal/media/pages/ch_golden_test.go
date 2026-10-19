package pages

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func chStr(v any) string {
	s, _ := v.(string)
	return s
}

func chText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

func chInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

func chBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	}
	return false
}

func chMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func chList(v any) []any {
	l, _ := v.([]any)
	return l
}

func chGoldenChannels(m map[string]any) ChannelsPage {
	page := ChannelsPage{Channels: []ChannelView{}, TMDB: chBool(m["tmdb"]), Q: chStr(m["q"])}
	for _, item := range chList(m["channels"]) {
		c := chMap(item)
		page.Channels = append(page.Channels, ChannelView{
			ID: chInt(c["id"]), Name: chStr(c["name"]), IMDb: chStr(c["imdb"]), Pattern: chStr(c["pattern"]),
			CaseSensitive: chBool(c["case_sensitive"]), Primed: chBool(c["primed"]),
		})
	}
	status := chMap(m["status"])
	page.Status.Errors = []string{}
	for _, e := range chList(status["errors"]) {
		page.Status.Errors = append(page.Status.Errors, chStr(e))
	}
	if stamp, ok := status["last_check"]; ok && stamp != nil {
		f := float64(chInt(stamp))
		page.Status.LastCheck = &f
	}
	if m["results"] != nil {
		hits := []SearchHit{}
		for _, item := range chList(m["results"]) {
			r := chMap(item)
			hits = append(hits, SearchHit{TmdbID: chInt(r["tmdb_id"]), Name: chStr(r["name"]), Year: chStr(r["year"])})
		}
		page.Results = &hits
	}
	f := chMap(m["form"])
	page.Form = ChannelForm{
		TmdbID: chText(f["tmdb_id"]), Name: chStr(f["name"]), IMDb: chStr(f["imdb"]), Pattern: chStr(f["pattern"]),
		CaseSensitive: chBool(f["case_sensitive"]), Backfill: chBool(f["backfill"]),
	}
	return page
}

func chGoldenPreview(m map[string]any) PreviewView {
	view := PreviewView{Error: chStr(m["error"]), Matched: int(chInt(m["matched"]))}
	if m["releases"] != nil {
		releases := []ReleaseView{}
		for _, item := range chList(m["releases"]) {
			r := chMap(item)
			releases = append(releases, ReleaseView{Title: chStr(r["title"]), Match: chBool(r["match"]), SizeBytes: chInt(r["size_bytes"])})
		}
		view.Releases = &releases
	}
	return view
}

func TestChGoldenPages(t *testing.T) {
	for _, name := range []string{"media_channels", "media_channels@empty", "media_channels@no-tmdb"} {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			f := goldenFrame(c)
			f.Page = "media_channels"
			f.Data = chGoldenChannels(c.Context)
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, "media_channels.gohtml", f); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}

func TestChGoldenPreview(t *testing.T) {
	for _, name := range []string{"_media_preview", "_media_preview@empty", "_media_preview@error"} {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			f := goldenFrame(c)
			f.Data = chGoldenPreview(c.Context)
			var out bytes.Buffer
			if err := goldenEngine(t, c).Fragment(&out, "_media_preview.gohtml", f); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}
