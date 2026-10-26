package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/integrations/transport"
	"github.com/jeiang/atrium/internal/media"
)

func TestIntegrationClientsUseTheRecordTransportAndAFailedRequestChecksAtOnce(t *testing.T) {
	qb := &fakeQbit{version: "v5.2.0"}
	var broken atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		qb.handler(w, r)
	}))
	t.Cleanup(srv.Close)

	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(media.KeyQbitURL), srv.URL)
	t.Setenv(settingEnv(media.KeyQbitAPIKey), "qbt_good")
	s := &integrationSite{t: t, dir: dir}
	s.start()
	waitFor(t, func() bool { return health(t, dir, "qbittorrent") == integrations.Failing })
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/media/downloads", nil); code == http.StatusOK && qb.saw("/api/v2/torrents/info") {
		t.Fatal("the downloads page reached a server with an untrusted certificate")
	}

	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runSettings(t, "set", transport.CAKey("qbittorrent"), ca); got.code != 0 {
		t.Fatalf("set CA: %d %q", got.code, got.stderr)
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/media/downloads", nil); code != http.StatusOK || !qb.saw("/api/v2/torrents/info") {
		t.Fatalf("downloads page with the qBittorrent CA: %d", code)
	}
	if code, body := s.post("/qbittorrent/test", nil); code != http.StatusOK || health(t, dir, "qbittorrent") != integrations.OK {
		t.Fatalf("qBittorrent Test with its CA: %d %s", code, body)
	}

	broken.Store(true)
	_, _, _ = fetch(t, s.client, http.MethodGet, s.base+"/media/downloads", nil)
	waitFor(t, func() bool { return health(t, dir, "qbittorrent") == integrations.Failing })
	if !strings.Contains(s.page(), "data-integration-alerts") {
		t.Error("no admin banner after a failed qBittorrent request")
	}
}
