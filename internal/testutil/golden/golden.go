package golden

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func Dir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("golden: cannot locate source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "golden")
}

func ReadFile(t testing.TB, slice, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(), slice, name))
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	return data
}
