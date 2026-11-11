package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/media"
)

func TestIntegrationClientsUseTheConnectionTransportAndAFailedRequestChecksAtOnce(t *testing.T) {
	requireCipher(t)
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

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(media.KeyQbitURL), srv.URL)
	s := &integrationSite{t: t, dir: dir}
	s.start()
	if state := connectionState(t, dir, "service/qbittorrent"); state == "" {
		t.Fatal("no qBittorrent connection was materialized from the host")
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/downloads", nil); code == http.StatusOK && qb.saw("/api/v2/torrents/info") {
		t.Fatal("the downloads page reached a server with an untrusted certificate")
	}
	waitFor(t, func() bool { return connectionState(t, dir, "service/qbittorrent") == "failing" })

	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_MEDIA_QBITTORRENT_CA_FILE", ca)
	s.start()
	if code, body := s.conn("/test", url.Values{"id": {"service/qbittorrent"}, "api_key": {"qbt_good"}}); code != http.StatusOK {
		t.Fatalf("qBittorrent Test with its CA: %d %s", code, body)
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/downloads", nil); code != http.StatusOK || !qb.saw("/api/v2/torrents/info") {
		t.Fatalf("downloads page with the qBittorrent CA: %d", code)
	}
	if got := runApp(t, "connections", "test", "service/qbittorrent"); got.code != 0 || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("connections test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}

	broken.Store(true)
	_, _, _ = fetch(t, s.client, http.MethodGet, s.base+"/apps/media/downloads", nil)
	waitFor(t, func() bool { return connectionState(t, dir, "service/qbittorrent") == "failing" })
}
