package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"testing"
)

var staticAssets = []struct {
	path, marker string
}{
	{"app.css", "--background"},
	{"app.js", "htmx"},
	{"basecoat/all.min.js", "basecoat"},
	{"htmx.min.js", "htmx"},
}

func TestStaticEmbedsAssets(t *testing.T) {
	for _, a := range staticAssets {
		b, err := fs.ReadFile(Static, a.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte(a.marker)) {
			t.Errorf("%s (%d bytes) lacks %q", a.path, len(b), a.marker)
		}
	}
}

func TestAssetVersionHashesFilesInPathOrder(t *testing.T) {
	digest := sha256.New()
	for _, a := range staticAssets {
		b, err := fs.ReadFile(Static, a.path)
		if err != nil {
			t.Fatal(err)
		}
		digest.Write(b)
	}
	if want := hex.EncodeToString(digest.Sum(nil))[:12]; AssetVersion != want {
		t.Errorf("AssetVersion = %s, want %s", AssetVersion, want)
	}
}
