package py

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	cache := filepath.Join(root, "cache")
	for _, d := range []string{outside, filepath.Join(cache, "raw")} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(cache, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("raw", filepath.Join(cache, "rel")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ in, want string }{
		{cache + "/raw/a.eml", cache + "/raw/a.eml"},
		{cache + "/raw" + strings.Repeat("/..", 40) + "/nonexistent/passwd", "/nonexistent/passwd"},
		{cache + "/link/../x", root + "/x"},
		{cache + "/rel/missing/../b", cache + "/raw/b"},
		{cache + "//raw/./c", cache + "/raw/c"},
		{"/..", "/"},
		{"/", "/"},
	} {
		if got := Resolve(tc.in); got != tc.want {
			t.Errorf("Resolve(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
	loop := filepath.Join(cache, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{loop: loop, loop + "/x/../y": loop + "/y"} {
		if got := Resolve(in); got != want {
			t.Errorf("Resolve(%s) = %s, want %s", in, got, want)
		}
	}
	if err := os.Symlink(".", filepath.Join(cache, "d")); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 40, 41, 100} {
		in := cache + "/" + strings.Repeat("d/", n) + strings.Repeat("../", 40) + "nonexistent/passwd"
		if got, want := Resolve(in), "/nonexistent/passwd"; got != want {
			t.Errorf("Resolve through %d self-links = %s, want %s", n, got, want)
		}
		if got, want := Resolve(cache+"/"+strings.Repeat("d/", n)+"raw/a.eml"), cache+"/raw/a.eml"; got != want {
			t.Errorf("Resolve through %d self-links = %s, want %s", n, got, want)
		}
	}
}
