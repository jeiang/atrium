package key

import "github.com/jeiang/atrium/internal/settings"

const fileKey = "key_file"

func init() {
	settings.MustRegister(settings.Def{Key: fileKey, Module: "db", Type: settings.String, Class: settings.Bootstrap})
}
