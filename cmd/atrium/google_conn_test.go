package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// googleHostConfig declares a host-owned google/default connection with file
// credentials so mail tests exercise the shared token source without an
// encrypted database.
func googleHostConfig(t *testing.T, dir string) {
	t.Helper()
	files := filepath.Join(dir, "google")
	if err := os.MkdirAll(files, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(files, "client_secret")
	refresh := filepath.Join(files, "gmail_refresh")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refresh, []byte("refresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := hostConfigBody(defaultWebPushConnection) + fmt.Sprintf(`
[connections."google/default"]
kind = "google"
label = "Google"
config = { client_id = "client-id" }
capabilities = ["gmail"]
credential_files = { "oauth/client_secret" = %q, "oauth/gmail/refresh" = %q }
`, secret, refresh)
	path := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", path)
}
