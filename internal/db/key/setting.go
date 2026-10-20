package key

import (
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const settingKey = "key"

func init() {
	settings.MustRegister(settings.Def{
		Key: settingKey, Module: "db", Type: settings.String, Class: settings.Secret, Secret: true, PinOnly: true,
		Check: func(value string) error {
			if _, err := db.ParseKey(value); err != nil {
				return settings.Invalid("must be 64 hex digits")
			}
			return nil
		},
	})
}
