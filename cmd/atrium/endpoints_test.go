package main

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
)

type fakeOpenAI struct {
	*httptest.Server
	mu     sync.Mutex
	dim    int
	status int
	auth   map[string][]string
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{dim: config.EmbedDim, auth: map[string][]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth[r.URL.Path] = append(f.auth[r.URL.Path], r.Header.Get("Authorization"))
		_, _ = io.ReadAll(r.Body)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = io.WriteString(w, `{"data":[{"id":"qwen"},{"id":"embeddinggemma"}]}`)
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			vec, _ := json.Marshal(make([]float64, f.dim))
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":`+string(vec)+`}]}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOpenAI) seen(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth[path]...)
}

func (f *fakeOpenAI) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeOpenAI) authorizations() map[string][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][]string, len(f.auth))
	for path, values := range f.auth {
		out[path] = append([]string(nil), values...)
	}
	return out
}

func TestRemovedChatEndpointInputsAreRefusedAndTheEmbeddingTestAloneGatesMail(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	const embedKey = "sk-embed-endpoint-0123456789"
	const chatSentinel = "sk-chat-file-sentinel-0123456789"
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	host := embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", embedKey)
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	run := func(name, value string) appRun {
		setEnv(t, name, value)
		got := runApp(t, "settings", "list")
		if name == "ATRIUM_HOST_CONFIG" {
			setEnv(t, name, host)
		} else {
			setEnv(t, name, "")
		}
		return got
	}
	keyPath := filepath.Join(dir, "chat.key")
	if err := os.WriteFile(keyPath, []byte(chatSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if probe := runApp(t, "endpoints", "probe"); probe.code == cli.ExitOK {
		t.Fatalf("endpoints probe still runs: stdout %q", probe.stdout)
	}
	for _, name := range []string{"ATRIUM_MAIL_MAIL_CHAT_URL", "ATRIUM_MAIL_MAIL_CHAT_MODEL", "ATRIUM_MAIL_MAIL_CHAT_KEY_FILE", "ATRIUM_MAIL_MAIL_CHAT_CA_FILE", "ATRIUM_MAIL_MAIL_CHAT_PROXY"} {
		value := "http://chat.invalid/v1"
		if strings.HasSuffix(name, "_FILE") {
			value = keyPath
		}
		got := run(name, value)
		if got.code == cli.ExitOK || !strings.Contains(got.stderr, name) || !strings.Contains(got.stderr, "service/anthropic") {
			t.Errorf("%s was not refused with its replacement: exit %d stderr %q", name, got.code, got.stderr)
		}
		if strings.Contains(got.stdout+got.stderr, chatSentinel) {
			t.Errorf("the %s refusal printed the key file: %q", name, got.stderr)
		}
	}
	for _, line := range []string{"mail_chat_url = \"http://chat.invalid/v1\"", fmt.Sprintf("mail_chat_key_file = %q", keyPath)} {
		hostPath := hostConfigFrom(t, host, dir, "host-chat.toml", nil, "\n[mail]\n"+line+"\n")
		got := run("ATRIUM_HOST_CONFIG", hostPath)
		if got.code == cli.ExitOK || !strings.Contains(got.stderr, "was removed") || !strings.Contains(got.stderr, "service/anthropic") {
			t.Errorf("host line %q was not refused with its replacement: exit %d stderr %q", line, got.code, got.stderr)
		}
		if strings.Contains(got.stdout+got.stderr, chatSentinel) {
			t.Errorf("the host refusal printed the key file: %q", got.stderr)
		}
	}
	if set := runSecrets(t, chatSentinel+"\n", "set", "mail_chat_key"); set.code == cli.ExitOK || strings.Contains(set.stdout+set.stderr, chatSentinel) {
		t.Errorf("secrets set mail_chat_key: exit %d stdout %q stderr %q", set.code, set.stdout, set.stderr)
	}
	if set := runSettings(t, "set", "mail_chat_model", "qwen"); set.code == cli.ExitOK {
		t.Errorf("settings set mail_chat_model was accepted: %q", set.stdout)
	}

	if ok := runApp(t, "connections", "test", "service/embedding"); ok.code != cli.ExitOK || strings.Contains(ok.stdout+ok.stderr, embedKey) {
		t.Fatalf("embedding Test: %d stdout %q stderr %q", ok.code, ok.stdout, ok.stderr)
	}
	for path, values := range embed.authorizations() {
		for _, got := range values {
			if got != "Bearer "+embedKey {
				t.Errorf("embed %s Authorization %q, want the embed bearer", path, got)
			}
		}
	}
	if len(embed.seen("/v1/chat/completions")) != 0 {
		t.Error("a chat completion request reached the embedding server")
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("enabling mail with no chat endpoint after a passing Test: %d %q", on.code, on.stderr)
	}

	embed.mu.Lock()
	embed.dim = 0
	embed.mu.Unlock()
	if failed := runApp(t, "connections", "test", "service/embedding"); failed.code != cli.ExitError || !strings.Contains(failed.stdout, "no embedding") {
		t.Fatalf("Test with an embedder that returns no vector: %d %q", failed.code, failed.stdout)
	}
	if off := runSettings(t, "set", module.FlagKey(module.Mail), "false"); off.code != cli.ExitOK {
		t.Fatalf("disabling mail: %d %q", off.code, off.stderr)
	}
	refused := runSettings(t, "set", module.FlagKey(module.Mail), "true")
	if refused.code != cli.ExitError || !strings.Contains(refused.stderr, "mail needs settings") || !strings.Contains(refused.stderr, "embed endpoint probe failed") {
		t.Fatalf("enabling mail with a failing Test: %d %q", refused.code, refused.stderr)
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v2", "embeddinggemma", "", embedKey))
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK {
		t.Fatalf("reconciling the changed host endpoint: %d %q", listed.code, listed.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Errorf("a changed endpoint is not held to the old failed Test: %d %q", on.code, on.stderr)
	}
}

func TestConnectionsTestUsesTheEmbedCAAndAPassingTestReleasesTheMailHold(t *testing.T) {
	t.Parallel()
	embed := newFakeOpenAI(t)
	tlsEmbed := httptest.NewTLSServer(embed.Config.Handler)
	t.Cleanup(tlsEmbed.Close)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, tlsEmbed.URL+"/v1", "embeddinggemma", ""))
	if untrusted := runApp(t, "connections", "test", "service/embedding"); untrusted.code != cli.ExitError || !strings.Contains(untrusted.stdout+untrusted.stderr, "certificate") {
		t.Fatalf("test without the embed CA: %d %q", untrusted.code, untrusted.stdout+untrusted.stderr)
	}
	ca := filepath.Join(dir, "embed-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsEmbed.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, tlsEmbed.URL+"/v1", "embeddinggemma", ca))
	embed.mu.Lock()
	embed.dim = 0
	embed.mu.Unlock()
	if failed := runApp(t, "connections", "test", "service/embedding"); failed.code != cli.ExitError || !strings.Contains(failed.stdout, "no embedding") {
		t.Fatalf("test with the CA and an embedder that returns no vector: %d %q", failed.code, failed.stdout)
	}
	if refused := runSettings(t, "set", module.FlagKey(module.Mail), "true"); refused.code != cli.ExitError || !strings.Contains(refused.stderr, "embed endpoint probe failed") {
		t.Fatalf("enabling mail with a failing probe: %d %q", refused.code, refused.stderr)
	}

	s := &connectionSite{t: t, dir: dir}
	s.start()
	if code, body := s.conn("/test", url.Values{"id": {"service/embedding"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "no embedding") {
		t.Fatalf("Test of an embedder that returns no vector: %d %s", code, body)
	}
	embed.mu.Lock()
	embed.dim = config.EmbedDim
	embed.mu.Unlock()
	if code, body := s.conn("/test", url.Values{"id": {"service/embedding"}}); code != http.StatusOK || connectionState(t, dir, "service/embedding") != "ok" {
		t.Fatalf("Test of the stored embed endpoint: %d %s", code, body)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("enabling mail after a passing Test: %d %q", on.code, on.stderr)
	}
}

func TestConnectionsTestReusesTheStoredEmbeddingBearerWhenTheInputIsBlank(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, embed.URL+"/v1", "embeddinggemma", ""))
	s := &connectionSite{t: t, dir: dir}
	s.start()
	if code, body := s.conn("/test", url.Values{"id": {"service/embedding"}, "credential.bearer": {"sk-embed-stored"}}); code != http.StatusOK {
		t.Fatalf("storing the embedding bearer: %d %.300s", code, body)
	}
	before := len(embed.seen("/v1/models"))
	if code, body := s.conn("/test", url.Values{"id": {"service/embedding"}}); code != http.StatusOK {
		t.Fatalf("re-testing with a blank bearer: %d %.300s", code, body)
	}
	seen := embed.seen("/v1/models")
	if len(seen) <= before {
		t.Fatal("the blank-bearer test did not probe the embedding endpoint")
	}
	for _, got := range seen[before:] {
		if got != "Bearer sk-embed-stored" {
			t.Fatalf("the blank-bearer test sent %q", got)
		}
	}
}
