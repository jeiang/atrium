//go:build !modernc

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestFirstHourKeyedOIDCBootstrap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyFile := writeKey(t, dir, "db-key", hex.EncodeToString(key))
	host := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(host, []byte("[core]\nops_listen = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
		"ATRIUM_DB_KEY": "", "ATRIUM_DB_KEY_FILE": keyFile,
		"CREDENTIALS_DIRECTORY": "", "ATRIUM_CONFIG": "", "ATRIUM_PUBLIC_HOST": "",
		"ATRIUM_HOST_CONFIG": host,
	} {
		setEnv(t, name, value)
	}
	start := func(name string, args ...string) *serveProcess {
		t.Helper()
		addrFile := filepath.Join(dir, name+"-addr")
		cmd := cliExitProcessArgs(t, append([]string{"serve", "--host", "127.0.0.1", "--port", "0"}, args...))
		cmd.Env = append(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_SERVE_ADDR_FILE="+addrFile)
		return startServeProcess(t, cmd, addrFile)
	}
	emergencyPort := freeLocalPort(t)
	public := start("public", "--auth", "oidc", "--allowed-host", "127.0.0.1", "--emergency-port", emergencyPort)
	base := "http://" + public.addr
	if code, _, _ := wimGet(t, base+"/login"); code != http.StatusOK {
		t.Fatalf("unconfigured sign-in status = %d, want 200", code)
	}
	if code, _, _ := wimGet(t, base+"/chat"); code != http.StatusSeeOther {
		t.Fatalf("unconfigured public chat status = %d, want 303", code)
	}
	for _, target := range []string{"/chat", "/settings/auth"} {
		if code, _, _ := wimGet(t, base+target, "Remote-User", "operator", "Remote-Groups", "atrium_access,atrium_admin"); code != http.StatusSeeOther {
			t.Fatalf("forged proxy headers on %s: status = %d, want 303", target, code)
		}
	}
	if code, _, _ := wimGet(t, "http://127.0.0.1:"+emergencyPort+"/settings/auth"); code != http.StatusOK {
		t.Fatalf("emergency settings status = %d, want 200", code)
	}
	if isPlaintextSQLite(t, path) {
		t.Fatal("first serve created a plaintext database")
	}
	second := runProcess(t, "serve --host 127.0.0.1 --port 0 --auth local")
	if second.code == cli.ExitOK || !strings.Contains(second.stderr, "in use by a running atrium serve") {
		t.Fatalf("concurrent serve exit = %d; want serve-lock refusal", second.code)
	}
	if code := public.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("public serve stop = %d, want 0", code)
	}
	local := start("local", "--auth", "local")
	if code, _, _ := wimGet(t, "http://"+local.addr+"/settings/auth"); code != http.StatusOK {
		t.Fatalf("local settings on the same keyed database = %d, want 200", code)
	}
	if code := local.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("local serve stop = %d, want 0", code)
	}
	if got := runProcess(t, "db check"); got.code != cli.ExitOK {
		t.Fatalf("keyed db check exit = %d, want 0", got.code)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := cliExitProcess(t, "db check")
	cmd.Env = envWithout(cmd.Env, "ATRIUM_DB_KEY_FILE=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if code := exitCode(cmd.Run()); code == cli.ExitOK || !strings.Contains(stderr.String(), "database file is encrypted but no key is configured") {
		t.Fatalf("unkeyed check exit = %d, stderr = %q", code, stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || isPlaintextSQLite(t, path) {
		t.Fatal("unkeyed db check changed the encrypted database")
	}
	missing := filepath.Join(dir, "missing.db")
	if got := runProcess(t, "db check", "ATRIUM_DB="+missing); got.code == cli.ExitOK || !strings.Contains(got.stderr, "no such file") {
		t.Fatalf("keyed missing database check exit = %d, stderr = %q", got.code, got.stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("db check created a database or failed to preserve its absence")
	}
	missing = filepath.Join(dir, "unreadable-key.db")
	got := runProcess(t, "serve --host 127.0.0.1 --port 0 --auth local", "ATRIUM_DB="+missing, "ATRIUM_DB_KEY_FILE="+filepath.Join(dir, "absent-key"))
	if got.code != cli.ExitConfig || !strings.Contains(got.stderr, "ATRIUM_DB_KEY_FILE") {
		t.Fatalf("unreadable key serve exit = %d, stderr = %q", got.code, got.stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("serve with an unreadable configured key created a database")
	}
}
