package mailtest

import (
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/settings"
)

func PinMode(t testing.TB, reg *settings.Registry, mode string) {
	t.Helper()
	pins, err := config.LoadPins(config.Map(map[string]string{config.EnvName(modeDef(t, reg)): mode}), reg)
	if err != nil {
		t.Fatal(err)
	}
	reg.SetPins(pins)
	t.Cleanup(func() { reg.SetPins(nil) })
}

func UseAccounts(t testing.TB, st *config.State, logins map[string]string) {
	t.Helper()
	var list []config.AccountSpec
	passwords := map[string]string{}
	for _, preset := range []string{config.ICloud, config.Gmail} {
		login, ok := logins[preset]
		if !ok {
			continue
		}
		a, err := config.NewAccount(preset, "", preset, login, true)
		if err != nil {
			t.Fatal(err)
		}
		passwords[a.Name] = "pw"
		list = append(list, a)
	}
	t.Cleanup(st.UseAccounts(list, passwords))
}

func modeDef(t testing.TB, reg *settings.Registry) settings.Def {
	t.Helper()
	d, ok := reg.Lookup(mail.KeyMode)
	if !ok {
		t.Fatal("mail mode is not registered")
	}
	return d
}
