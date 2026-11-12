package key

import (
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const settingKey = "key"

func Settings() []settings.Def {
	return []settings.Def{settings.Secret(settings.SecretOpts{PinOnly: true}, settings.Def{
		Key: settingKey, Module: "db", Type: settings.String, Check: func(value string) error {
			if _, err := db.ParseKey(value); err != nil {
				return settings.Invalid("must be 64 hex digits")
			}
			return nil
		}})}
}
