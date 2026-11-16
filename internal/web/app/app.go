package app

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sync"
)

//go:embed dist
var dist embed.FS

var FS = mustSub(dist, "dist")

var BuildID = sync.OnceValue(func() string {
	sum := sha256.New()
	err := fs.WalkDir(FS, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fs.ReadFile(FS, name)
		if err != nil {
			return err
		}
		sum.Write([]byte(name))
		sum.Write([]byte{0})
		sum.Write(content)
		sum.Write([]byte{0})
		return nil
	})
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
})

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
