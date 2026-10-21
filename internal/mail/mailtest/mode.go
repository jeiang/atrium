package mailtest

import (
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/settings"
)

func PinMode(t testing.TB, mode string) {
	t.Helper()
	pins, err := config.LoadPins(config.Map(map[string]string{config.EnvName(modeDef(t)): mode}), settings.Default)
	if err != nil {
		t.Fatal(err)
	}
	settings.Default.SetPins(pins)
	t.Cleanup(func() { settings.Default.SetPins(nil) })
}

func modeDef(t testing.TB) settings.Def {
	t.Helper()
	d, ok := settings.Default.Lookup(mail.KeyMode)
	if !ok {
		t.Fatal("mail mode is not registered")
	}
	return d
}
