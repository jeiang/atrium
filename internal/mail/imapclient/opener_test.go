package imapclient

import (
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
)

func TestOpenerNeedsCredentials(t *testing.T) {
	dir := t.TempDir()
	env := config.Map(map[string]string{"ATRIUM_DB": dir + "/atrium.db", "ATRIUM_CACHE_DIR": dir + "/cache"})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{config.ICloud, config.Gmail, "other"} {
		_, err := Opener(cfg, env)(t.Context(), account)
		if err == nil || err.Error() != "missing credentials for "+account || py.ExceptionClass(err) != "RuntimeError" {
			t.Errorf("%s: err = %v", account, err)
		}
	}
}
