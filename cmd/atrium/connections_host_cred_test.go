package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestHostCredentialFileReplacesADatabaseCredential(t *testing.T) {
	gate := &qbitGate{token: "qbt_file_secret"}
	srv := httptest.NewTLSServer(http.HandlerFunc(gate.handler))
	t.Cleanup(srv.Close)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfig(t, dir, srv.URL, ca, ""))
	if ran := runApp(t, "connections", "list"); ran.code != cli.ExitOK {
		t.Fatalf("connections list: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO connection_credentials (connection_id, slot, source, value, set_at, set_by) "+
		"VALUES ('service/qbittorrent', 'api_key', 'database', 'qbt_db_stale', ?, 'operator')", float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "qbittorrent.key")
	if err := os.WriteFile(keyFile, []byte("qbt_file_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfig(t, dir, srv.URL, ca, keyFile))
	tested := runApp(t, "connections", "test", "service/qbittorrent")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("connections test with the host file: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	var source string
	var value *string
	if err := d.Read.QueryRow("SELECT source, value FROM connection_credentials WHERE connection_id = 'service/qbittorrent' AND slot = 'api_key'").
		Scan(&source, &value); err != nil {
		t.Fatal(err)
	}
	if source != "host_file" || value != nil {
		t.Fatalf("credential source %q value %v, want the host file only", source, value)
	}
	if err := os.WriteFile(keyFile, []byte("qbt_rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate.mu.Lock()
	gate.token = "qbt_rotated"
	gate.mu.Unlock()
	tested = runApp(t, "connections", "test", "service/qbittorrent")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("connections test after rotation: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
}
