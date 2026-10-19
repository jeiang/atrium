package pages

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

var dlPageGoldens = []string{"media_add", "media_add@plain", "media_downloads", "media_downloads@empty"}

var dlFragmentGoldens = []string{
	"_media_torrents", "_media_torrents@empty", "_media_files", "_media_files@empty", "_media_files@error",
}

func TestDlGoldenPages(t *testing.T) {
	for _, name := range dlPageGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			base := strings.SplitN(name, "@", 2)[0]
			f := goldenFrame(c)
			f.Page = base
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, base+".gohtml", f); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}

func TestDlGoldenFragments(t *testing.T) {
	for _, name := range dlFragmentGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Fragment(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c)); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}
