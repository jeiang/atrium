package features

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func FuzzExtract(f *testing.F) {
	paths, err := filepath.Glob(filepath.Join(rxtest.Dir(), "features", "*.eml"))
	if err != nil || len(paths) == 0 {
		f.Fatalf("no seed corpus: %v", err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		i := strings.Index(string(b), "\r\n\r\n")
		if i < 0 {
			i = len(b)
		}
		f.Add(b[:i], b[i:])
	}
	f.Fuzz(func(t *testing.T, header, text []byte) {
		if _, err := Extract(header, text, nil, 0); err != nil && strings.Contains(err.Error(), "internal error") {
			t.Fatal(err)
		}
	})
}
