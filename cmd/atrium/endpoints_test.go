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
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/integrations/transport"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/module"
)

type fakeOpenAI struct {
	*httptest.Server
	mu    sync.Mutex
	dim   int
	auth  map[string][]string
	chats int
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{dim: config.EmbedDim, auth: map[string][]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth[r.URL.Path] = append(f.auth[r.URL.Path], r.Header.Get("Authorization"))
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = io.WriteString(w, `{"data":[{"id":"qwen"},{"id":"embeddinggemma"}]}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			f.chats++
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"ok\": true}"}}]}`)
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

func TestEndpointProbeGatesTheMailModuleAndSendsTheKeyOnlyToItsEndpoint(t *testing.T) {
	const chatKey = "sk-chat-endpoint-0123456789"
	chat, embed := newFakeOpenAI(t), newFakeOpenAI(t)
	moduleFixture(t, flagsExcept(module.Mail, ""))
	t.Setenv(settingEnv(mail.KeyChatURL), chat.URL+"/v1")
	t.Setenv(settingEnv(mail.KeyEmbedURL), embed.URL+"/v1")
	t.Setenv(settingEnv(mail.KeyChatKey), chatKey)

	ok := runApp("endpoints", "probe")
	if ok.code != cli.ExitOK || strings.Contains(ok.stdout+ok.stderr, chatKey) || strings.Contains(ok.stderr, "hosted") {
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
		if got != "" {
			t.Errorf("embed request Authorization %q, want none", got)
		}
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("enabling mail after passing probes: %d %q", on.code, on.stderr)
	}

	embed.mu.Lock()
	embed.dim = 1024
	embed.mu.Unlock()
	if failed := runApp("endpoints", "probe"); failed.code != cli.ExitError || !strings.Contains(failed.stdout, "1024 dimensions") {
		t.Fatalf("probe with a 1024-dimension embedder: %d %q", failed.code, failed.stdout)
	}
	if off := runSettings(t, "set", module.FlagKey(module.Mail), "false"); off.code != cli.ExitOK {
		t.Fatalf("disabling mail: %d %q", off.code, off.stderr)
	}
	refused := runSettings(t, "set", module.FlagKey(module.Mail), "true")
	if refused.code != cli.ExitError || !strings.Contains(refused.stderr, "mail needs settings") || !strings.Contains(refused.stderr, "embed endpoint probe failed") {
		t.Fatalf("enabling mail with a failing probe: %d %q", refused.code, refused.stderr)
	}
	before := chat.chats
	if export := runApp("rules", "export"); export.code != cli.ExitOK || export.stdout != "" || chat.chats != before {
		t.Errorf("rules export while mail needs settings: %d %q", export.code, export.stdout)
	}

	t.Setenv(settingEnv(mail.KeyEmbedURL), embed.URL+"/v2")
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Errorf("a changed endpoint is not held to the old failed probe: %d %q", on.code, on.stderr)
	}
}

func TestEndpointFormTestUsesTheEndpointCAAndAPassingTestReleasesTheMailHold(t *testing.T) {
	chat, embed := newFakeOpenAI(t), newFakeOpenAI(t)
	tlsEmbed := httptest.NewTLSServer(embed.Config.Handler)
	t.Cleanup(tlsEmbed.Close)
	dir, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	unsetEnv(t, settingEnv(mail.KeyChatURL), settingEnv(mail.KeyEmbedURL))
	for key, value := range map[string]string{mail.KeyChatURL: chat.URL + "/v1", mail.KeyEmbedURL: tlsEmbed.URL + "/v1"} {
		if got := runSettings(t, "set", key, value); got.code != cli.ExitOK {
			t.Fatalf("set %s: %d %q", key, got.code, got.stderr)
		}
	}
	if untrusted := runApp("endpoints", "probe"); untrusted.code != cli.ExitError || !strings.Contains(untrusted.stdout, "certificate") {
		t.Fatalf("probe without the embed CA: %d %q", untrusted.code, untrusted.stdout)
	}
	ca := filepath.Join(dir, "embed-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsEmbed.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runSettings(t, "set", transport.CAKey(mail.Integration(mail.EndpointEmbed)), ca); got.code != cli.ExitOK {
		t.Fatalf("set embed CA: %d %q", got.code, got.stderr)
	}
	embed.mu.Lock()
	embed.dim = 1024
	embed.mu.Unlock()
	if failed := runApp("endpoints", "probe"); failed.code != cli.ExitError || !strings.Contains(failed.stdout, "1024 dimensions") {
		t.Fatalf("probe with the CA and a 1024-dimension embedder: %d %q", failed.code, failed.stdout)
	}
	if refused := runSettings(t, "set", module.FlagKey(module.Mail), "true"); refused.code != cli.ExitError || !strings.Contains(refused.stderr, "embed endpoint probe failed") {
		t.Fatalf("enabling mail with a failing probe: %d %q", refused.code, refused.stderr)
	}

	s := &integrationSite{t: t, dir: dir}
	s.start()
	if code, body := s.post("/mail_embed/test", url.Values{"url": {embed.URL + "/v1"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "1024 dimensions") {
		t.Fatalf("Test of a 1024-dimension embedder: %d %s", code, body)
	}
	embed.mu.Lock()
	embed.dim = config.EmbedDim
	embed.mu.Unlock()
	if code, body := s.post("/mail_embed/test", nil); code != http.StatusOK || !strings.Contains(body, "768 dimensions") || health(t, dir, mail.Integration(mail.EndpointEmbed)) != integrations.OK {
		t.Fatalf("Test of the stored embed endpoint: %d %s", code, body)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("enabling mail after a passing Test: %d %q", on.code, on.stderr)
	}
}
