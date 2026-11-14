//go:build !modernc

package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/media"
)

func TestMediaLibraryAPIRange(t *testing.T) {
	const rel = "movies/" + libraryMovie + "/" + libraryMovie + ".mkv"
	dir := t.TempDir()
	qb, qbURL := mnpNewQbit(t)
	agent, root := agentLibrary(t, dir, qbURL)
	body := strings.Repeat("0123456789abcdefghijklmnop", 3<<20)
	mnpWrite(t, filepath.Join(root, rel), []byte(body))
	seedAllFlagsIn(t, dir)
	token := mediaAPIToken(t)
	s := &apiSite{t: t, base: startServeIn(t, dir)}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	path := "/api/v1/media/files/" + mediaAPIID(rel) + "/content"
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	request := func(method, path, bearer string, payload io.Reader, contentType string, headers ...string) apiReply {
		t.Helper()
		req, err := http.NewRequest(method, s.base+path, payload)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		for i := 0; i < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range resp.Header {
			for _, v := range value {
				if strings.Contains(v, root) || strings.Contains(v, agent.dir) {
					t.Fatal("header leaks path")
				}
			}
		}
		return apiReply{status: resp.StatusCode, body: string(data), header: resp.Header}
	}
	get := request("GET", path, token, nil, "", "Range", "bytes=3-8")
	if get.status != 206 || get.body != body[3:9] || get.header.Get("Content-Range") != fmt.Sprintf("bytes 3-8/%d", len(body)) {
		t.Fatalf("range: %+v", get)
	}
	bad := request("GET", path, token, nil, "", "Range", "bytes=999999999-")
	if bad.status != 416 || bad.header.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(body)) {
		t.Fatalf("range refusal: %+v", bad)
	}
	head := request("HEAD", path, token, nil, "", "Accept-Encoding", "gzip")
	if head.status != 200 || head.body != "" || head.header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("head: %+v", head)
	}
	full := request("GET", path, token, nil, "", "Accept-Encoding", "gzip")
	if full.status != 200 || full.header.Get("Content-Encoding") != "" || full.header.Get("Content-Length") != head.header.Get("Content-Length") || full.body != body {
		t.Fatalf("full file: status %d, encoding %q, length %q, %d bytes", full.status, full.header.Get("Content-Encoding"), full.header.Get("Content-Length"), len(full.body))
	}
	for _, art := range []string{"/api/v1/media/art/season/" + mediaAPIID(libraryMovie), "/api/v1/media/art/movie/" + mediaAPIID(libraryMovie) + "?season=1"} {
		if got := request("GET", art, token, nil, ""); got.status != 422 || !strings.Contains(got.body, `"season"`) {
			t.Fatalf("art season %s: %+v", art, got)
		}
	}
	if got := request("GET", path, "", nil, "", "Remote-User", "operator"); got.status != 401 {
		t.Fatalf("bearer-only: %+v", got)
	}
	for _, suffix := range []string{"", "?exp=1&sig=invalid"} {
		public := media.FilePrefix + mnpQuote(rel) + suffix
		for _, bearer := range []string{"", token} {
			got := request("GET", public, bearer, nil, "", "Remote-User", "operator", "Remote-Groups", "atrium_access")
			if got.status != 403 {
				t.Fatalf("public signature required for operator: %+v", got)
			}
		}
	}
	upload := func(data []byte, magnet string) apiReply {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		part, err := writer.CreateFormFile("torrent", "upload.torrent")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if magnet != "" {
			if err := writer.WriteField("magnet", magnet); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return request("POST", "/api/v1/media/torrents", token, &buf, writer.FormDataContentType())
	}
	torrent := []byte("d4:infod6:lengthi1e4:name8:test.mkv12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	hash, err := media.TorrentHash(torrent)
	if err != nil {
		t.Fatal(err)
	}
	added := upload(torrent, "")
	if added.status != 200 || qb.count("add") != 1 {
		t.Fatalf("torrent add: %+v, adds %d", added, qb.count("add"))
	}
	mediaEffectID(t, d, media.OpAddFile, hash, "done")
	for _, input := range [][]byte{[]byte("not a torrent"), bytes.Repeat([]byte("a"), media.TuningMediaTorrentUploadBytes)} {
		if got := upload(input, ""); got.status != 422 || qb.count("add") != 1 {
			t.Fatalf("invalid torrent: %+v", got)
		}
	}
	if got := upload(torrent, mediaMagnet(hash, "duplicate")); got.status != 422 || qb.count("add") != 1 {
		t.Fatalf("xor: %+v", got)
	}
	if got := upload([]byte("d4:infod9:file treede12:meta versioni2e4:name8:test.mkv12:piece lengthi16384eee"), ""); got.status != 422 || !strings.Contains(got.body, "no v1 info hash") || qb.count("add") != 1 {
		t.Fatalf("v2-only torrent: %+v", got)
	}
	hybrid := []byte("d4:infod9:file treede6:lengthi1e12:meta versioni2e4:name9:test2.mkv12:piece lengthi16384e6:pieces20:bbbbbbbbbbbbbbbbbbbbee")
	hybridHash, err := media.TorrentHash(hybrid)
	if err != nil {
		t.Fatal(err)
	}
	if got := upload(hybrid, ""); got.status != 200 || qb.count("add") != 2 {
		t.Fatalf("hybrid torrent: %+v, adds %d", got, qb.count("add"))
	}
	mediaEffectID(t, d, media.OpAddFile, hybridHash, "done")
	for _, id := range []string{mediaAPIID("downloads/secret.mkv"), mediaAPIID("movies/../secret.mkv"), mediaAPIID("movies/" + libraryMovie + "/missing.mkv")} {
		if got := request("GET", "/api/v1/media/files/"+id+"/content", token, nil, ""); got.status != 404 {
			t.Fatalf("bad id: %+v", got)
		}
	}
	stream, err := http.NewRequest("GET", s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(stream)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	_ = agent.cmd.Process.Kill()
	<-agent.done
	rest, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err == nil {
		t.Fatalf("a broken stream completed: %d of %d bytes", 1<<20+len(rest), len(body))
	}
	down := request("GET", path, token, nil, "")
	if down.status != 503 || strings.Contains(down.body, body) || strings.Contains(down.body, root) {
		t.Fatalf("agent down: %+v", down)
	}
}

func TestMediaRelayRefusesWhenMediaIsOffAtBoot(t *testing.T) {
	const rel = "movies/" + libraryMovie + "/" + libraryMovie + ".mkv"
	_, _, f := moduleFixture(t, flagsExcept("media", "false"))
	token := localAPIToken(t, "media-off")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	s := &apiSite{t: t, base: base}
	id := mediaAPIID(rel)
	for _, path := range []string{"/api/v1/media/files/" + id + "/content", "/api/v1/media/files/" + id + "/thumbnail", "/api/v1/media/art/movie/" + id} {
		expectAPI(t, s.do(http.MethodGet, path, token, nil), http.StatusConflict, "module_disabled")
	}
	if got := s.do(http.MethodHead, "/api/v1/media/files/"+id+"/content", token, nil); got.status != http.StatusConflict {
		t.Fatalf("HEAD content with media off: %d", got.status)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/media/torrents", token, map[string]any{"magnet": "magnet:?xt=urn:btih:" + strings.Repeat("a", 40)}, "Idempotency-Key", "add-1"), http.StatusConflict, "module_disabled")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/media/filings/"+strings.Repeat("a", 40), token, map[string]any{"action": "file"}, "Idempotency-Key", "file-1"), http.StatusConflict, "module_disabled")
}
