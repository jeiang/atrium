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
	body := "0123456789abcdefghijklmnop"
	mnpWrite(t, filepath.Join(root, rel), []byte(body))
	seedAllFlagsIn(t, dir)
	token := mediaAPIToken(t)
	s := &apiSite{t: t, base: startServeIn(t, dir)}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	path := "/api/v1/media/files/" + mediaAPIID(rel) + "/content"
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
		resp, err := http.DefaultClient.Do(req)
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
	bad := request("GET", path, token, nil, "", "Range", "bytes=999-")
	if bad.status != 416 || bad.header.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(body)) {
		t.Fatalf("range refusal: %+v", bad)
	}
	head := request("HEAD", path, token, nil, "")
	if head.status != 200 || head.body != "" || head.header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("head: %+v", head)
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
	for _, id := range []string{mediaAPIID("downloads/secret.mkv"), mediaAPIID("movies/../secret.mkv"), mediaAPIID("movies/" + libraryMovie + "/missing.mkv")} {
		if got := request("GET", "/api/v1/media/files/"+id+"/content", token, nil, ""); got.status != 404 {
			t.Fatalf("bad id: %+v", got)
		}
	}
	agent.terminate(t)
	down := request("GET", path, token, nil, "")
	if down.status != 503 || strings.Contains(down.body, body) || strings.Contains(down.body, root) {
		t.Fatalf("agent down: %+v", down)
	}
}
