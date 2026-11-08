package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
)

func TestMediaSweepOnlyDeletesMediaTorrents(t *testing.T) {
	const mediaHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const plainHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	torrents := []media.Torrent{
		{Hash: mediaHash, Name: "Filed.Show", State: media.StoppedUP, Tags: "media,selected,filed"},
		{Hash: plainHash, Name: "Filed.Plain", State: media.StoppedUP, Tags: "filed"},
	}
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			out := torrents
			if r.URL.Query().Get("tag") == media.TagMedia {
				out = torrents[:1]
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/api/v2/torrents/delete":
			_ = r.ParseForm()
			deleted = append(deleted, r.Form.Get("hashes"))
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	_, _ = moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(media.KeyQbitURL), srv.URL)
	ran := runApp("media", "sweep")
	if ran.code != cli.ExitOK {
		t.Fatalf("media sweep: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	if len(deleted) != 1 || deleted[0] != mediaHash {
		t.Fatalf("deleted %v, want only %s", deleted, mediaHash)
	}
}
