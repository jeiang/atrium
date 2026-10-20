package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
)

//go:embed static/app.css static/app.js static/htmx.min.js static/basecoat/all.min.js
var staticFiles embed.FS

var Static = mustSub(staticFiles, "static")

var AssetVersion = assetVersion(Static)

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func assetVersion(fsys fs.FS) string {
	digest := sha256.New()
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		digest.Write(b)
		return nil
	})
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(digest.Sum(nil))[:12]
}
