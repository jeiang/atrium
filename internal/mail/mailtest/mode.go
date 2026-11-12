package mailtest

import (
	"testing"

	"github.com/jeiang/atrium/internal/config"
)

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
