package main

import (
	"encoding/json"
	"encoding/pem"
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
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/module"
)

type fakeOpenAI struct {
	*httptest.Server
	mu         sync.Mutex
	dim        int
	auth       map[string][]string
	chats      int
	chatReply  string
	chatBodies []map[string]any
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{dim: config.EmbedDim, auth: map[string][]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth[r.URL.Path] = append(f.auth[r.URL.Path], r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = io.WriteString(w, `{"data":[{"id":"qwen"},{"id":"embeddinggemma"}]}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			f.chats++
			var payload map[string]any
			if json.Unmarshal(raw, &payload) == nil {
				f.chatBodies = append(f.chatBodies, payload)
			}
			reply := f.chatReply
			if reply == "" {
				reply = `{"ok": true}`
			}
			content, _ := json.Marshal(reply)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":`+string(content)+`}}]}`)
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

func (f *fakeOpenAI) setChatReply(reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatReply = reply
}

func (f *fakeOpenAI) chatCalls() (int, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chats, append([]map[string]any(nil), f.chatBodies...)
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

func TestEndpointProbeGatesTheMailModuleAndSendsTheKeysOnlyToTheirEndpoints(t *testing.T) {
	t.Parallel()
	const chatKey = "sk-chat-endpoint-0123456789"
	const embedKey = "sk-embed-endpoint-0123456789"
	chat, embed := newFakeOpenAI(t), newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	setEnv(t, settingEnv(endpoint.KeyChatURL), chat.URL+"/v1")
	setEnv(t, settingEnv(endpoint.KeyChatKey), chatKey)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", embedKey))

	ok := runApp(t, "endpoints", "probe")
	if ok.code != cli.ExitOK || strings.Contains(ok.stdout+ok.stderr, chatKey) || strings.Contains(ok.stdout+ok.stderr, embedKey) || strings.Contains(ok.stderr, "hosted") {
		t.Fatalf("probe: %d stdout %q stderr %q", ok.code, ok.stdout, ok.stderr)
	}
	var results []mail.ProbeResult
	if err := json.Unmarshal([]byte(ok.stdout), &results); err != nil || len(results) != 2 || !results[0].OK || !results[1].OK ||
		results[1].Dimension != config.EmbedDim || strings.Join(results[0].Models, ",") != "embeddinggemma,qwen" {
		t.Fatalf("probe results %+v (%v)", results, err)
	}
	for _, got := range chat.seen("/v1/chat/completions") {
		if got != "Bearer "+chatKey {
			t.Errorf("chat request Authorization %q, want the chat key", got)
		}
	}
	for _, got := range embed.seen("/v1/embeddings") {
		if got != "Bearer "+embedKey {
			t.Errorf("embed request Authorization %q, want the embed bearer", got)
		}
	}
	for path, values := range chat.authorizations() {
		for _, got := range values {
			if strings.Contains(got, embedKey) {
				t.Errorf("chat %s received the embed bearer", path)
			}
		}
	}
	for path, values := range embed.authorizations() {
		for _, got := range values {
			if strings.Contains(got, chatKey) {
				t.Errorf("embed %s received the chat key", path)
			}
		}
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("enabling mail after passing probes: %d %q", on.code, on.stderr)
	}

	embed.mu.Lock()
	embed.dim = 0
	embed.mu.Unlock()
	if failed := runApp(t, "endpoints", "probe"); failed.code != cli.ExitError || !strings.Contains(failed.stdout, "no embedding") {
		t.Fatalf("probe with an embedder that returns no vector: %d %q", failed.code, failed.stdout)
	}
	if off := runSettings(t, "set", module.FlagKey(module.Mail), "false"); off.code != cli.ExitOK {
		t.Fatalf("disabling mail: %d %q", off.code, off.stderr)
	}
	refused := runSettings(t, "set", module.FlagKey(module.Mail), "true")
	if refused.code != cli.ExitError || !strings.Contains(refused.stderr, "mail needs settings") || !strings.Contains(refused.stderr, "embed endpoint probe failed") {
		t.Fatalf("enabling mail with a failing probe: %d %q", refused.code, refused.stderr)
	}
	before := chat.chats
	if export := runApp(t, "rules", "export"); export.code != cli.ExitOK || export.stdout != "" || chat.chats != before {
		t.Errorf("rules export while mail needs settings: %d %q", export.code, export.stdout)
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v2", "embeddinggemma", "", embedKey))
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK {
		t.Fatalf("reconciling the changed host endpoint: %d %q", listed.code, listed.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Errorf("a changed endpoint is not held to the old failed probe: %d %q", on.code, on.stderr)
	}
}

func TestConnectionsTestUsesTheEmbedCAAndAPassingTestReleasesTheMailHold(t *testing.T) {
	t.Parallel()
	chat, embed := newFakeOpenAI(t), newFakeOpenAI(t)
	tlsEmbed := httptest.NewTLSServer(embed.Config.Handler)
	t.Cleanup(tlsEmbed.Close)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	unsetEnv(t, settingEnv(endpoint.KeyChatURL))
	if got := runSettings(t, "set", endpoint.KeyChatURL, chat.URL+"/v1"); got.code != cli.ExitOK {
		t.Fatalf("set chat URL: %d %q", got.code, got.stderr)
	}
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
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	requireCipher(t)
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
