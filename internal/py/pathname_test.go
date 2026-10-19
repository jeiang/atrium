package py

import "testing"

func TestPathName(t *testing.T) {
	for in, want := range map[string]string{
		"": "", ".": "", "/": "", "a": "a", "a/b": "b", "a/b/": "b", "../../etc/passwd": "passwd", "a/./b": "b", "a//b": "b", "..": "..", "x/.": "x", ".hidden": ".hidden",
	} {
		if got := PathName(in); got != want {
			t.Errorf("PathName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "a": "a"[:0], "a.txt": ".txt", "a.tar.gz": ".gz", ".bashrc": "", "a.": ".", "..": "", "...": "", "a..b": ".b", ".a.b": ".b", "名前.txt": ".txt",
	} {
		if got := PathSuffix(in); got != want {
			t.Errorf("PathSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitExt(t *testing.T) {
	for _, c := range []struct{ in, root, ext string }{
		{"a.txt", "a", ".txt"}, {"a", "a", ""}, {".bashrc", ".bashrc", ""}, {"..x", "..x", ""}, {"dir.d/file", "dir.d/file", ""},
		{"dir/a.b.c", "dir/a.b", ".c"}, {"a.", "a", "."}, {"...", "...", ""}, {"", "", ""}, {"..a.b", "..a", ".b"}, {"/x/.y.z", "/x/.y", ".z"},
	} {
		if root, ext := SplitExt(c.in); root != c.root || ext != c.ext {
			t.Errorf("SplitExt(%q) = %q, %q; want %q, %q", c.in, root, ext, c.root, c.ext)
		}
	}
}
