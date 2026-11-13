package web

import (
	"embed"
	"encoding/json"
	"io/fs"
)

//go:embed static
var staticFiles embed.FS

var Static = mustSub(staticFiles, "static")

var Assets = mustAssets(Static)

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func mustAssets(fsys fs.FS) map[string]string {
	raw, err := fs.ReadFile(fsys, "assets.json")
	if err != nil {
		panic(err)
	}
	var assets map[string]string
	if err := json.Unmarshal(raw, &assets); err != nil {
		panic(err)
	}
	return assets
}
