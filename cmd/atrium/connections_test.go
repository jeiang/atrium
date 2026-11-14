package main

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

type qbitGate struct {
	mu      sync.Mutex
	status  int
	hold    chan struct{}
	started chan struct{}
	token   string
}

func (g *qbitGate) signal() {
	g.mu.Lock()
	started := g.started
	g.mu.Unlock()
	if started == nil {
		return
	}
	select {
	case <-started:
	default:
		close(started)
	}
}

func (g *qbitGate) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	status, hold, token := g.status, g.hold, g.token
	g.mu.Unlock()
	g.signal()
	if hold != nil {
		<-hold
	}
	if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if r.URL.Path != "/api/v2/app/version" {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte("v5.2.0"))
}

func (g *qbitGate) set(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status = status
}

func hostConfig(t *testing.T, dir, url, ca, keyFile string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("[connections.\"service/qbittorrent\"]\n")
	b.WriteString("kind = \"service/qbittorrent\"\n")
	b.WriteString("label = \"qBittorrent\"\n")
	b.WriteString("config = { url = " + fmt.Sprintf("%q", url) + " }\n")
	b.WriteString("transport = { ca_file = " + fmt.Sprintf("%q", ca) + " }\n")
	b.WriteString("capabilities = [\"api\"]\n")
	if keyFile != "" {
		b.WriteString("credential_files = { api_key = " + fmt.Sprintf("%q", keyFile) + " }\n")
	}
	path := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConnectionsCLIListAndTestUseTheHostTransportAndNeverStoreTheFileValue(t *testing.T) {
	t.Parallel()
	gate := &qbitGate{token: "qbt_file_secret"}
	srv := httptest.NewTLSServer(http.HandlerFunc(gate.handler))
	t.Cleanup(srv.Close)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "qbittorrent.key")
	if err := os.WriteFile(keyFile, []byte("qbt_file_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfig(t, dir, srv.URL, ca, keyFile))

	listed := runApp(t, "connections", "list", "--json")
	if listed.code != cli.ExitOK {
		t.Fatalf("connections list: exit %d stderr %q", listed.code, listed.stderr)
	}
	var views []struct {
		ID          string `json:"id"`
		State       string `json:"state"`
		Credentials []struct {
			Slot    string `json:"slot"`
			Source  string `json:"source"`
			Present bool   `json:"present"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal([]byte(listed.stdout), &views); err != nil {
		t.Fatalf("connections list JSON: %v\n%s", err, listed.stdout)
	}
	var qbit *struct {
		ID          string `json:"id"`
		State       string `json:"state"`
		Credentials []struct {
			Slot    string `json:"slot"`
			Source  string `json:"source"`
			Present bool   `json:"present"`
		} `json:"credentials"`
	}
	for i := range views {
		if views[i].ID == "service/qbittorrent" {
			qbit = &views[i]
		}
	}
	if qbit == nil || len(qbit.Credentials) != 1 ||
		qbit.Credentials[0].Source != "host_file" || !qbit.Credentials[0].Present {
		t.Fatalf("connections list = %+v", views)
	}
	if strings.Contains(listed.stdout, "qbt_file_secret") {
		t.Fatal("connections list printed the host file value")
	}

	tested := runApp(t, "connections", "test", "service/qbittorrent")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("connections test: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	if got := connectionState(t, dir, "service/qbittorrent"); got != "ok" {
		t.Fatalf("connection state = %q", got)
	}

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	var value, fileRef *string
	if err := d.Read.QueryRow("SELECT value, file_ref FROM connection_credentials WHERE connection_id = 'service/qbittorrent' AND slot = 'api_key'").Scan(&value, &fileRef); err != nil {
		t.Fatal(err)
	}
	if value != nil || fileRef == nil || *fileRef != keyFile {
		t.Fatalf("credential value %v file_ref %v, want a host file reference only", value, fileRef)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "qbt_file_secret") {
		t.Fatal("the host file value entered the database")
	}
}

func TestConnectionsHTTPTestAndSavePreservesThePriorConfigOnFailure(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	gate := &qbitGate{}
	srv := httptest.NewTLSServer(http.HandlerFunc(gate.handler))
	t.Cleanup(srv.Close)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfig(t, dir, srv.URL, ca, ""))
	s := &connectionSite{t: t, dir: dir}
	s.start()

	if code, body := s.conn("/test", url.Values{"id": {"service/qbittorrent"}, "api_key": {"qbt_good"}}); code != http.StatusOK {
		t.Fatalf("qBittorrent Test: %d %s", code, body)
	}
	if got := connectionState(t, dir, "service/qbittorrent"); got != "ok" {
		t.Fatalf("state after a passing test = %q", got)
	}
	gate.set(http.StatusInternalServerError)
	if code, body := s.conn("/test", url.Values{"id": {"service/qbittorrent"}, "api_key": {"qbt_other"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "test failed") {
		t.Fatalf("failing Test: %d", code)
	}
	if got := connectionState(t, dir, "service/qbittorrent"); got != "ok" {
		t.Fatalf("a failed candidate replaced the working state: %q", got)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	var value string
	if err := d.Read.QueryRow("SELECT value FROM connection_credentials WHERE connection_id = 'service/qbittorrent' AND slot = 'api_key'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "qbt_good" {
		t.Fatalf("a failed candidate replaced the stored credential: %q", value)
	}
}

func TestConnectionsStaleTestCannotOverwriteARotation(t *testing.T) {
	t.Parallel()
	gate := &qbitGate{hold: make(chan struct{}), started: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(gate.handler))
	t.Cleanup(srv.Close)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfig(t, dir, srv.URL, "", ""))
	if got := runApp(t, "connections", "list"); got.code != cli.ExitOK {
		t.Fatalf("connections list: %d %s", got.code, got.stderr)
	}

	done := make(chan appRun, 1)
	go func() { done <- runApp(t, "connections", "test", "service/qbittorrent") }()
	<-gate.started
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec("UPDATE connections SET revision = revision + 1 WHERE id = 'service/qbittorrent'"); err != nil {
		t.Fatal(err)
	}
	close(gate.hold)
	ran := <-done
	if ran.code != cli.ExitOK {
		t.Fatalf("connections test: exit %d stderr %q", ran.code, ran.stderr)
	}
	var state string
	if err := d.Read.QueryRow("SELECT state FROM connections WHERE id = 'service/qbittorrent'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "ok" {
		t.Fatal("a stale successful test overwrote a newer revision")
	}
}

func TestConnectionsRefuseToStoreACredentialInAPlaintextDatabase(t *testing.T) {
	gate := &qbitGate{}
	srv := httptest.NewTLSServer(http.HandlerFunc(gate.handler))
	t.Cleanup(srv.Close)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfig(t, dir, srv.URL, ca, ""))
	s := &connectionSite{t: t, dir: dir}
	s.start()
	if code, body := s.conn("/test", url.Values{"id": {"service/qbittorrent"}, "api_key": {"qbt_good"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "encrypted database") {
		t.Fatalf("a credential save into a plaintext database: %d %.300s", code, body)
	}
	var n int
	if err := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)).Read.QueryRow(
		"SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'service/qbittorrent' AND slot = 'api_key'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the plaintext database stored %d credentials", n)
	}
}
