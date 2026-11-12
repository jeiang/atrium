//go:build !modernc

package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
)

func plainHostConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "host-plain.toml")
	body := hostConfigBodyWith("", defaultWebPushConnection, filepath.Join(testRoot, "host", "anthropic.key"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func operatorEmbedding(t *testing.T, dir, endpoint, token string) {
	t.Helper()
	s := &connectionSite{t: t, dir: dir}
	s.start()
	form := url.Values{"id": {"service/embedding"}, "url": {endpoint}, "credential.bearer": {token}}
	if code, body := s.conn("/test", form); code != http.StatusOK && code != http.StatusSeeOther {
		t.Fatalf("saving the operator embedding: %d %s", code, body)
	}
	s.stop()
}

func embeddingRow(t *testing.T, dir string) (revision int64, configJSON, credentialSource, credentialValue string) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	if err := d.Read.QueryRow("SELECT revision, config_json FROM connections WHERE id = 'service/embedding'").
		Scan(&revision, &configJSON); err != nil {
		t.Fatal(err)
	}
	var stored *string
	if err := d.Read.QueryRow("SELECT source, value FROM connection_credentials WHERE connection_id = 'service/embedding' AND slot = 'bearer'").
		Scan(&credentialSource, &stored); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		credentialValue = *stored
	}
	return revision, configJSON, credentialSource, credentialValue
}

func endpointAuthorizations(f *fakeOpenAI) []string {
	var out []string
	for _, seen := range f.authorizations() {
		out = append(out, seen...)
	}
	return out
}

func TestAHostTakeoverCannotMoveAStoredCredentialToAnotherEndpoint(t *testing.T) {
	requireCipher(t)
	a, b := newFakeOpenAI(t), newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	unsetEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL")
	setEnv(t, "ATRIUM_HOST_CONFIG", plainHostConfig(t, dir))
	operatorEmbedding(t, dir, a.URL+"/v1", "token-a")
	revision, configJSON, source, value := embeddingRow(t, dir)

	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, b.URL+"/v1", "embeddinggemma", "", ""))
	listed := runApp(t, "connections", "list")
	if listed.code == cli.ExitOK {
		t.Fatalf("the identity-changing takeover booted: %q", listed.stdout)
	}
	for _, want := range []string{"service/embedding", "declare a credential file", "disconnect the stored credential"} {
		if !strings.Contains(listed.stderr+listed.stdout, want) {
			t.Errorf("the refusal %q lacks %q", listed.stderr+listed.stdout, want)
		}
	}
	nextRevision, nextConfig, nextSource, nextValue := embeddingRow(t, dir)
	if nextRevision != revision || nextConfig != configJSON || nextSource != source || nextValue != value {
		t.Errorf("the refused takeover changed the row: revision %d->%d config %q->%q credential %s/%q->%s/%q",
			revision, nextRevision, configJSON, nextConfig, source, value, nextSource, nextValue)
	}
	if seen := endpointAuthorizations(b); len(seen) != 0 {
		t.Errorf("the stored credential reached the declared endpoint: %v", seen)
	}
}

func TestAHostTakeoverWithTheSameEndpointKeepsTheStoredCredential(t *testing.T) {
	requireCipher(t)
	a, b := newFakeOpenAI(t), newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	unsetEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL")
	setEnv(t, "ATRIUM_HOST_CONFIG", plainHostConfig(t, dir))
	operatorEmbedding(t, dir, a.URL+"/v1", "token-a")

	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, a.URL+"/v1", "embeddinggemma", "", ""))
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK {
		t.Fatalf("the same-endpoint takeover: %d %q", listed.code, listed.stderr)
	}
	if _, _, source, _ := embeddingRow(t, dir); source != "database" {
		t.Fatalf("the retained credential is %q, not database", source)
	}
	tested := runApp(t, "connections", "test", "service/embedding")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("testing the taken-over connection: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	if seen := endpointAuthorizations(a); !containsString(seen, "Bearer token-a") {
		t.Errorf("the retained credential was not used at the same endpoint: %v", seen)
	}
	if seen := endpointAuthorizations(b); len(seen) != 0 {
		t.Errorf("the retained credential reached another endpoint: %v", seen)
	}
}

func TestAHostTakeoverWithACredentialFileForEverySlotIsAllowed(t *testing.T) {
	requireCipher(t)
	a, b := newFakeOpenAI(t), newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	unsetEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL")
	setEnv(t, "ATRIUM_HOST_CONFIG", plainHostConfig(t, dir))
	operatorEmbedding(t, dir, a.URL+"/v1", "token-a")

	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, b.URL+"/v1", "embeddinggemma", "", "token-file"))
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK {
		t.Fatalf("the credential-file takeover: %d %q", listed.code, listed.stderr)
	}
	_, _, source, value := embeddingRow(t, dir)
	if source != "host_file" || value != "" {
		t.Fatalf("the replaced credential is %s/%q", source, value)
	}
	tested := runApp(t, "connections", "test", "service/embedding")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("testing the taken-over connection: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	seen := endpointAuthorizations(b)
	if !containsString(seen, "Bearer token-file") || containsString(seen, "Bearer token-a") {
		t.Errorf("the declared endpoint saw %v", seen)
	}
	if seen := endpointAuthorizations(a); containsString(seen, "Bearer token-file") {
		t.Errorf("the host credential reached the replaced endpoint: %v", seen)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
